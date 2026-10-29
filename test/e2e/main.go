// Command e2e validates the Major Project-I success criteria against a
// running BranchBox + Online Boutique stack:
//
//	C1  untagged requests are served entirely by the baseline
//	C2  a tagged request is routed to its sandbox only at the correct hop
//	C3  the environment ID survives every intermediate call
//	C4  concurrent sandboxes (including two of the same service) never interfere
//	C5  after teardown, traffic falls back to the baseline unaffected
//
// Each simulated shopper runs a full journey (home page, add to cart,
// checkout) under one W3C trace ID, then the routing events recorded by the
// proxies for that trace are checked hop by hop.
//
//	go run ./test/e2e            (from the repository root)
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"branchbox/internal/api"
)

var (
	edgeURL       = flag.String("edge", "http://127.0.0.1:8080", "BranchBox edge URL")
	controllerURL = flag.String("controller", "http://127.0.0.1:8090", "BranchBox controller URL")
	rounds        = flag.Int("rounds", 5, "concurrent rounds in the interference test")
	keep          = flag.Bool("keep", false, "keep the test sandboxes instead of tearing them down")
	token         = flag.String("token", os.Getenv("BRANCHBOX_TOKEN"), "API token of a shared BranchBox server")
)

// sandboxPlan is a sandbox the test needs, with the evidence its change
// leaves in the shop's HTML.
type sandboxPlan struct {
	id, service, branch string
	homeMarker          string // must appear on the home page
	trackingPrefix      string // order tracking ID prefix, if changed
}

var plans = []sandboxPlan{
	{id: "e2e-sale", service: "productcatalogservice", branch: "feature/diwali-sale", homeMarker: "Diwali Sale"},
	{id: "e2e-clear", service: "productcatalogservice", branch: "feature/clearance-50", homeMarker: "Clearance"},
	{id: "e2e-ship", service: "shippingservice", branch: "feature/express-shipping", trackingPrefix: "EXP-"},
}

// deep hops: (source, dest) pairs that prove propagation beyond the first hop.
var deepHops = map[string][][2]string{
	"productcatalogservice": {{"checkoutservice", "productcatalogservice"}, {"recommendationservice", "productcatalogservice"}},
	"shippingservice":       {{"checkoutservice", "shippingservice"}},
}

type journey struct {
	env      string
	traceID  string
	home     string
	tracking string
	total    string
	err      error
}

type checker struct {
	mu       sync.Mutex
	failures map[string][]string
	counts   map[string]int
}

func (c *checker) fail(criterion, format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[criterion] = append(c.failures[criterion], fmt.Sprintf(format, args...))
}

func (c *checker) count(criterion string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[criterion] += n
}

func main() {
	flag.Parse()
	chk := &checker{failures: map[string][]string{}, counts: map[string]int{}}

	step("Checking the controller and edge")
	must(getJSON(*controllerURL+"/healthz", nil))

	step("Provisioning test sandboxes (reused if already ready)")
	addrs := ensureSandboxes()
	byEnv := map[string]sandboxPlan{}
	for _, p := range plans {
		byEnv[p.id] = p
		fmt.Printf("    %-10s %-22s %-26s %s\n", p.id, p.service, p.branch, addrs[p.id])
	}
	time.Sleep(time.Second) // let every proxy observe the new route table

	step("C1-C3: one journey per environment")
	envs := append([]string{""}, ids()...)
	var first []journey
	for _, env := range envs {
		first = append(first, shop(env))
	}
	verifyAll(chk, first, byEnv, addrs, false)

	step(fmt.Sprintf("C4: %d rounds of %d concurrent journeys", *rounds, len(envs)))
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		all []journey
	)
	for r := 0; r < *rounds; r++ {
		for _, env := range envs {
			wg.Add(1)
			go func(env string) {
				defer wg.Done()
				j := shop(env)
				mu.Lock()
				all = append(all, j)
				mu.Unlock()
			}(env)
		}
	}
	wg.Wait()
	verifyAll(chk, all, byEnv, addrs, true)

	if *keep {
		step("Skipping teardown (-keep)")
	} else {
		step("C5: tearing down the test sandboxes")
		for _, p := range plans {
			must(send(http.MethodDelete, *controllerURL+"/v1/sandboxes/"+p.id, nil, nil))
		}
		time.Sleep(time.Second)
		var rt api.RouteTable
		must(getJSON(*controllerURL+"/v1/routes", &rt))
		for _, r := range rt.Routes {
			if byEnv[r.Env].id != "" {
				chk.fail("C5", "route %s/%s still published after delete", r.Service, r.Env)
			}
		}
		var after []journey
		for _, env := range envs {
			after = append(after, shop(env))
		}
		time.Sleep(1500 * time.Millisecond)
		for _, j := range after {
			events := traceEvents(j)
			chk.count("C5", len(events))
			if j.err != nil {
				chk.fail("C5", "journey env=%q failed after teardown: %v", j.env, j.err)
			}
			for _, ev := range events {
				if ev.Decision != api.DecisionBaseline {
					chk.fail("C5", "env=%q %s->%s still routed to %s after teardown", j.env, ev.Source, ev.Dest, ev.Target)
				}
			}
			checkBaselineHTML(chk, "C5", j)
		}
	}

	report(chk)
}

// verifyAll applies C1-C4 to a set of journeys.
func verifyAll(chk *checker, journeys []journey, byEnv map[string]sandboxPlan, addrs map[string]string, concurrent bool) {
	time.Sleep(1500 * time.Millisecond) // proxies flush events every 300ms
	for _, j := range journeys {
		events := traceEvents(j)
		crit := func(c string) string {
			if concurrent {
				return "C4"
			}
			return c
		}
		if j.err != nil {
			chk.fail(crit("C2"), "journey env=%q failed: %v", j.env, j.err)
			continue
		}
		if len(events) < 15 {
			chk.fail(crit("C1"), "env=%q: only %d hops recorded for trace %s", j.env, len(events), j.traceID)
		}
		if j.env == "" {
			chk.count(crit("C1"), len(events))
			for _, ev := range events {
				if ev.Decision != api.DecisionBaseline || ev.Env != "" {
					chk.fail(crit("C1"), "untagged %s->%s went to %s (env %q)", ev.Source, ev.Dest, ev.Target, ev.Env)
				}
			}
			checkBaselineHTML(chk, crit("C1"), j)
			continue
		}

		plan := byEnv[j.env]
		chk.count(crit("C2"), len(events))
		chk.count(crit("C3"), len(events))
		deep := 0
		for _, ev := range events {
			if ev.Env != j.env {
				chk.fail(crit("C3"), "env=%s lost at %s->%s (saw %q)", j.env, ev.Source, ev.Dest, ev.Env)
			}
			if ev.Dest == plan.service {
				if ev.Decision != api.DecisionSandbox || ev.Target != addrs[plan.id] {
					chk.fail(crit("C2"), "env=%s %s->%s went to %s, want sandbox %s", j.env, ev.Source, ev.Dest, ev.Target, addrs[plan.id])
				}
				for _, h := range deepHops[plan.service] {
					if ev.Source == h[0] && ev.Dest == h[1] && ev.Decision == api.DecisionSandbox {
						deep++
					}
				}
			} else if ev.Decision != api.DecisionBaseline {
				chk.fail(crit("C2"), "env=%s %s->%s diverted to %s but only %s is sandboxed", j.env, ev.Source, ev.Dest, ev.Target, plan.service)
			}
		}
		if deep == 0 {
			chk.fail(crit("C3"), "env=%s: no sandbox decision observed beyond the first hop", j.env)
		}
		if plan.homeMarker != "" && !strings.Contains(j.home, plan.homeMarker) {
			chk.fail(crit("C2"), "env=%s: home page does not show %q", j.env, plan.homeMarker)
		}
		if plan.homeMarker == "" && (strings.Contains(j.home, "Diwali Sale") || strings.Contains(j.home, "Clearance")) {
			chk.fail(crit("C2"), "env=%s: home page shows another sandbox's catalog", j.env)
		}
		if plan.trackingPrefix != "" && !strings.HasPrefix(j.tracking, plan.trackingPrefix) {
			chk.fail(crit("C2"), "env=%s: tracking ID %q lacks prefix %q", j.env, j.tracking, plan.trackingPrefix)
		}
		if plan.trackingPrefix == "" && strings.HasPrefix(j.tracking, "EXP-") {
			chk.fail(crit("C2"), "env=%s: got another sandbox's express tracking ID %q", j.env, j.tracking)
		}
	}
}

func checkBaselineHTML(chk *checker, crit string, j journey) {
	if strings.Contains(j.home, "Diwali Sale") || strings.Contains(j.home, "Clearance") {
		chk.fail(crit, "env=%q: home page shows a sandbox catalog", j.env)
	}
	if strings.HasPrefix(j.tracking, "EXP-") {
		chk.fail(crit, "env=%q: got sandbox tracking ID %q", j.env, j.tracking)
	}
	if j.total != "$28.98" {
		chk.fail(crit, "env=%q: total %q, want baseline $28.98", j.env, j.total)
	}
}

var (
	trackingRe = regexp.MustCompile(`Tracking #[\s\S]*?<div[^>]*>\s*([A-Z0-9-]+)\s*<`)
	totalRe    = regexp.MustCompile(`Total Paid[\s\S]*?<div[^>]*>\s*(\$[0-9.,]+)\s*<`)
)

// shop runs one complete shopper journey under a fresh trace ID.
func shop(env string) journey {
	j := journey{env: env, traceID: randHex(16)}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	do := func(method, path string, form url.Values) (string, error) {
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, err := http.NewRequest(method, *edgeURL+path, body)
		if err != nil {
			return "", err
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Traceparent", "00-"+j.traceID+"-"+randHex(8)+"-01")
		if env != "" {
			req.Header.Set("Baggage", "branchbox-env="+env)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return string(b), fmt.Errorf("%s %s: %s", method, path, resp.Status)
		}
		return string(b), nil
	}
	var err error
	if j.home, err = do(http.MethodGet, "/", nil); err != nil {
		j.err = err
		return j
	}
	if _, err = do(http.MethodPost, "/cart", url.Values{"product_id": {"OLJCESPC7Z"}, "quantity": {"1"}}); err != nil {
		j.err = err
		return j
	}
	order, err := do(http.MethodPost, "/cart/checkout", url.Values{
		"email": {"someone@example.com"}, "street_address": {"1600 Amphitheatre Parkway"}, "zip_code": {"94043"},
		"city": {"Mountain View"}, "state": {"CA"}, "country": {"United States"},
		"credit_card_number": {"4432801561520454"}, "credit_card_expiration_month": {"1"},
		"credit_card_expiration_year": {"2030"}, "credit_card_cvv": {"672"},
	})
	if err != nil {
		j.err = err
		return j
	}
	if m := trackingRe.FindStringSubmatch(order); m != nil {
		j.tracking = m[1]
	}
	if m := totalRe.FindStringSubmatch(order); m != nil {
		j.total = m[1]
	}
	if j.tracking == "" {
		j.err = fmt.Errorf("checkout page has no tracking ID")
	}
	return j
}

func traceEvents(j journey) []api.Event {
	var events []api.Event
	if err := getJSON(*controllerURL+"/v1/traces/"+j.traceID, &events); err != nil {
		return nil
	}
	return events
}

// ensureSandboxes creates the planned sandboxes in parallel (or reuses
// ready ones) and returns their addresses.
func ensureSandboxes() map[string]string {
	addrs := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range plans {
		wg.Add(1)
		go func(p sandboxPlan) {
			defer wg.Done()
			var sb api.Sandbox
			if err := getJSON(*controllerURL+"/v1/sandboxes/"+p.id, &sb); err != nil || sb.Status == api.StatusFailed {
				if err == nil {
					must(send(http.MethodDelete, *controllerURL+"/v1/sandboxes/"+p.id, nil, nil))
				}
				must(send(http.MethodPost, *controllerURL+"/v1/sandboxes", api.CreateSandboxRequest{ID: p.id, Service: p.service, Branch: p.branch}, &sb))
			}
			deadline := time.Now().Add(15 * time.Minute)
			for sb.Status != api.StatusReady {
				if sb.Status == api.StatusFailed || time.Now().After(deadline) {
					fmt.Fprintf(os.Stderr, "sandbox %s did not become ready: %s %s\n", p.id, sb.Status, sb.Error)
					os.Exit(1)
				}
				time.Sleep(2 * time.Second)
				must(getJSON(*controllerURL+"/v1/sandboxes/"+p.id, &sb))
			}
			mu.Lock()
			addrs[p.id] = sb.Address
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	return addrs
}

func report(chk *checker) {
	names := []struct{ id, text string }{
		{"C1", "Untagged requests are served entirely by the baseline"},
		{"C2", "Tagged requests are routed to the sandbox only at the correct hop"},
		{"C3", "The environment ID survives every intermediate call"},
		{"C4", "Concurrent sandboxes never interfere (incl. two of the same service)"},
		{"C5", "After teardown, traffic falls back to the baseline"},
	}
	fmt.Println("\n================ BranchBox MP-I validation ================")
	failed := false
	for _, n := range names {
		status := "PASS"
		if len(chk.failures[n.id]) > 0 {
			status, failed = "FAIL", true
		}
		if n.id == "C5" && *keep {
			status = "SKIP"
		}
		fmt.Printf("  %s  %s  %s  (%d hops checked)\n", status, n.id, n.text, chk.counts[n.id])
		f := chk.failures[n.id]
		sort.Strings(f)
		for i, msg := range f {
			if i == 8 {
				fmt.Printf("          ... and %d more\n", len(f)-8)
				break
			}
			fmt.Println("          -", msg)
		}
	}
	fmt.Println("===========================================================")
	if failed {
		os.Exit(1)
	}
}

func ids() []string {
	out := make([]string, len(plans))
	for i, p := range plans {
		out[i] = p.id
	}
	return out
}

func getJSON(u string, out any) error { return send(http.MethodGet, u, nil, out) }

func send(method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %s %s", method, u, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if strings.HasSuffix(u, "/healthz") {
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func step(s string) { fmt.Printf("==> %s\n", s) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
