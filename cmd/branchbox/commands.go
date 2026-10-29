package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"branchbox/internal/api"
)

type client struct {
	base  string
	token string
	info  *api.Info
}

// edge returns the installation's public settings (fetched once).
func (c *client) edge() api.Info {
	if c.info == nil {
		c.info = &api.Info{EdgeDomain: "localhost", EdgePort: "8080"}
		_ = c.get("/v1/info", c.info)
	}
	return *c.info
}

// baselineURL is where the unmodified application is served.
func (c *client) baselineURL() string {
	e := c.edge()
	return fmt.Sprintf("http://%s:%s", e.EdgeDomain, e.EdgePort)
}

// sandboxLink switches a browser into sandbox id (cookie-based; works on
// any network without DNS setup).
func (c *client) sandboxLink(id string) string {
	return c.baselineURL() + "/__branchbox/env/" + id
}

// sandboxHost is the per-sandbox hostname, or "" when the edge domain is a
// bare IP address (no wildcard DNS available).
func (c *client) sandboxHost(id string) string {
	e := c.edge()
	if net.ParseIP(e.EdgeDomain) != nil {
		return ""
	}
	return fmt.Sprintf("http://%s.%s:%s", id, e.EdgeDomain, e.EdgePort)
}

func (c *client) status() error {
	var cfg struct {
		Project    string `json:"project"`
		Repository struct {
			URL     string `json:"url"`
			BaseRef string `json:"baseRef"`
		} `json:"repository"`
		Entry struct {
			Service string `json:"service"`
		} `json:"entry"`
	}
	if err := c.get("/v1/project", &cfg); err != nil {
		return err
	}
	var rt api.RouteTable
	if err := c.get("/v1/routes", &rt); err != nil {
		return err
	}
	fmt.Printf("Controller   %s (reachable)\n", c.base)
	fmt.Printf("Project      %s\n", cfg.Project)
	fmt.Printf("Repository   %s (baseline: %s)\n", cfg.Repository.URL, cfg.Repository.BaseRef)
	fmt.Printf("Entry        %s via %s\n", cfg.Entry.Service, c.baselineURL())
	fmt.Printf("Services     %d\n", len(rt.Services))
	fmt.Printf("Routes       %d active (table version %d)\n", len(rt.Routes), rt.Version)
	if len(rt.Routes) > 0 {
		w := table()
		fmt.Fprintln(w, "\n  SERVICE\tENV ID\tSANDBOX ADDRESS")
		for _, r := range rt.Routes {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Service, r.Env, r.Address)
		}
		w.Flush()
	}
	return nil
}

func (c *client) services() error {
	var rt api.RouteTable
	if err := c.get("/v1/routes", &rt); err != nil {
		return err
	}
	active := map[string]int{}
	for _, r := range rt.Routes {
		active[r.Service]++
	}
	w := table()
	fmt.Fprintln(w, "SERVICE\tPORT\tPROTOCOL\tACTIVE SANDBOXES")
	for _, s := range rt.Services {
		fmt.Fprintf(w, "%s\t%d\t%s\t%d\n", s.Name, s.Port, s.Protocol, active[s.Name])
	}
	return w.Flush()
}

func (c *client) createSandbox(service, branch, id string, wait bool) error {
	var sb api.Sandbox
	if err := c.send(http.MethodPost, "/v1/sandboxes", api.CreateSandboxRequest{ID: id, Service: service, Branch: branch}, &sb); err != nil {
		return err
	}
	fmt.Printf("Creating sandbox %s: %s from branch %q\n", sb.ID, sb.Service, sb.Branch)
	if !wait {
		fmt.Printf("Follow progress with: branchbox sandbox get %s\n", sb.ID)
		return nil
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	start, last := time.Now(), api.SandboxStatus("")
	for {
		if err := c.get("/v1/sandboxes/"+sb.ID, &sb); err != nil {
			return err
		}
		if sb.Status != last {
			fmt.Printf("  [%5.1fs] %s\n", time.Since(start).Seconds(), describe(sb))
			last = sb.Status
		}
		switch sb.Status {
		case api.StatusReady:
			c.printReady(sb)
			return nil
		case api.StatusFailed:
			return fmt.Errorf("sandbox %s failed: %s\n       build log: branchbox sandbox logs %s --component build", sb.ID, sb.Error, sb.ID)
		}
		select {
		case <-interrupt:
			fmt.Printf("\nStopped waiting; %s keeps provisioning in the background.\n", sb.ID)
			return nil
		case <-time.After(time.Second):
		}
	}
}

func describe(sb api.Sandbox) string {
	switch sb.Status {
	case api.StatusPending:
		return "pending: queued"
	case api.StatusBuilding:
		return "building: fetching branch and building the image"
	case api.StatusStarting:
		return "starting: launching sidecar and service containers"
	case api.StatusReady:
		return "ready"
	}
	return string(sb.Status)
}

func (c *client) printReady(sb api.Sandbox) {

	fmt.Printf("\nSandbox %s is ready in %.1fs\n", sb.ID, sb.SetupSeconds())
	fmt.Printf("  service   %s (branch %s, commit %s)\n", sb.Service, sb.Branch, short(sb.Commit))
	fmt.Printf("  address   %s\n\n", sb.Address)
	fmt.Println("Send traffic through it:")
	fmt.Printf("  browser   %s   (switches this browser into the sandbox)\n", c.sandboxLink(sb.ID))
	if host := c.sandboxHost(sb.ID); host != "" {
		fmt.Printf("            %s   (where wildcard DNS works)\n", host)
	}
	fmt.Printf("  curl      curl -H \"baggage: branchbox-env=%s\" %s/\n", sb.ID, c.baselineURL())
	fmt.Println("Untagged traffic keeps using the baseline.")
}

func (c *client) listSandboxes() error {
	var list []api.Sandbox
	if err := c.get("/v1/sandboxes", &list); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("No sandboxes. Create one with: branchbox sandbox create --service S --branch B")
		return nil
	}
	w := table()
	fmt.Fprintln(w, "ID\tSERVICE\tBRANCH\tCOMMIT\tSTATUS\tSETUP\tAGE")
	for _, sb := range list {
		setup := "-"
		if sb.ReadyAt != nil {
			setup = fmt.Sprintf("%.1fs", sb.SetupSeconds())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", sb.ID, sb.Service, sb.Branch, short(sb.Commit), sb.Status, setup, age(sb.CreatedAt))
	}
	return w.Flush()
}

func (c *client) getSandbox(id string) error {
	var sb api.Sandbox
	if err := c.get("/v1/sandboxes/"+id, &sb); err != nil {
		return err
	}
	out, _ := json.MarshalIndent(sb, "", "  ")
	fmt.Println(string(out))
	if sb.Status == api.StatusReady {
		c.printReady(sb)
	}
	return nil
}

func (c *client) logs(id, component string, tail int, follow bool) error {
	q := url.Values{"component": {component}, "tail": {fmt.Sprint(tail)}}
	if follow {
		q.Set("follow", "1")
	}
	req, err := http.NewRequest(http.MethodGet, c.base+"/v1/sandboxes/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	c.authorize(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

func (c *client) deleteSandboxes(ids []string, all bool) error {
	if all {
		var list []api.Sandbox
		if err := c.get("/v1/sandboxes", &list); err != nil {
			return err
		}
		ids = ids[:0]
		for _, sb := range list {
			ids = append(ids, sb.ID)
		}
		if len(ids) == 0 {
			fmt.Println("No sandboxes to delete.")
		}
	}
	for _, id := range ids {
		if err := c.send(http.MethodDelete, "/v1/sandboxes/"+id, nil, nil); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		fmt.Printf("Deleted %s (its traffic now uses the baseline)\n", id)
	}
	return nil
}

func (c *client) trace(traceID string) error {
	var events []api.Event
	if err := c.get("/v1/traces/"+url.PathEscape(traceID), &events); err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("no routing events recorded for trace %s", traceID)
	}
	printEvents(events, true)
	sandboxed := 0
	for _, ev := range events {
		if ev.Decision == api.DecisionSandbox {
			sandboxed++
		}
	}
	fmt.Printf("\n%d hops, %d served by a sandbox, %d by the baseline\n", len(events), sandboxed, len(events)-sandboxed)
	return nil
}

func (c *client) events(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	env := fs.String("env", "", "only events for this environment ID")
	limit := fs.Int("limit", 50, "maximum number of events")
	parseInterleaved(fs, args)
	q := url.Values{"limit": {fmt.Sprint(*limit)}}
	if *env != "" {
		q.Set("env", *env)
	}
	var events []api.Event
	if err := c.get("/v1/events?"+q.Encode(), &events); err != nil {
		return err
	}
	printEvents(events, false)
	return nil
}

func printEvents(events []api.Event, relative bool) {
	w := table()
	fmt.Fprintln(w, "TIME\tTRACE\tENV\tFROM\tTO\tROUTED TO\tTARGET\tSTATUS\tMS\tCALL")
	t0 := events[0].Time
	for _, ev := range events {
		when := ev.Time.Local().Format("15:04:05.000")
		if relative {
			when = fmt.Sprintf("+%dms", ev.Time.Sub(t0).Milliseconds())
		}
		from := ev.Source
		if ev.SourceSandbox != "" {
			from += "@" + ev.SourceSandbox
		}
		routed := "baseline"
		if ev.Decision == api.DecisionSandbox {
			routed = "SANDBOX"
		}
		status := fmt.Sprint(ev.Status)
		if ev.GRPCStatus != "" {
			status += "/grpc " + ev.GRPCStatus
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.1f\t%s %s\n", when, short(ev.TraceID), dash(ev.Env), from, ev.Dest,
			routed, ev.Target, status, ev.DurationMs, ev.Method, ev.Path)
	}
	w.Flush()
}

// --- HTTP helpers -----------------------------------------------------------

func (c *client) get(path string, out any) error { return c.send(http.MethodGet, path, nil, out) }

func (c *client) send(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the BranchBox controller at %s (is it running?): %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *client) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func apiError(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	return fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
}

// --- formatting -------------------------------------------------------------

func table() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0) }

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return dash(sha)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func age(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d > time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.String()
}
