package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"branchbox/internal/api"
)

func (c *client) production(args []string) error {
	if len(args) == 0 {
		return c.productionStatus()
	}
	switch args[0] {
	case "status":
		return c.productionStatus()
	case "update":
		fs := flag.NewFlagSet("production update", flag.ExitOnError)
		noWait := fs.Bool("no-wait", false, "return immediately")
		parseInterleaved(fs, args[1:])
		return c.productionUpdate(!*noWait)
	case "logs":
		if len(args) != 2 {
			return fmt.Errorf("usage: branchbox production logs SERVICE")
		}
		req, err := http.NewRequest(http.MethodGet, c.base+"/v1/production/logs/"+args[1], nil)
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
	return fmt.Errorf("unknown production command %q (status, update, logs)", args[0])
}

func (c *client) productionStatus() error {
	var p api.Production
	if err := c.get("/v1/production", &p); err != nil {
		return err
	}
	fmt.Printf("Production   %s @ %s  %q\n", p.Branch, short(p.Commit), p.Subject)
	fmt.Printf("Status       %s\n", p.Status)
	if p.LastError != "" {
		fmt.Printf("Problem      %s\n", p.LastError)
	}
	if p.AutoUpdate {
		fmt.Printf("Auto-update  on: checks %s every %ds; merged changes deploy automatically\n", p.Branch, p.PollSeconds)
	} else {
		fmt.Printf("Auto-update  off: run 'branchbox production update' after merging\n")
	}
	if p.LastChecked != nil {
		fmt.Printf("Last check   %s ago\n", age(*p.LastChecked))
	}

	w := table()
	fmt.Fprintln(w, "\nSERVICE\tRUNNING\tIMAGE")
	for _, s := range p.Services {
		source := "prebuilt upstream image (unchanged code)"
		if s.Source == "branch" {
			source = "built from " + p.Branch
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, source, s.Image)
	}
	w.Flush()

	if len(p.History) > 0 {
		fmt.Println("\nRecent production updates:")
		w = table()
		fmt.Fprintln(w, "WHEN\tTRIGGER\tCHANGE\tSERVICES UPDATED\tTIME\tRESULT")
		for i := len(p.History) - 1; i >= 0 && i >= len(p.History)-5; i-- {
			h := p.History[i]
			result := "ok"
			if h.Error != "" {
				result = "error: " + h.Error
			}
			services := strings.Join(h.Services, ", ")
			if services == "" {
				services = "-"
			}
			fmt.Fprintf(w, "%s ago\t%s\t%s -> %s %q\t%s\t%.0fs\t%s\n", age(h.Time), h.Trigger,
				dash(short(h.From)), short(h.To), h.Subject, services, h.Seconds, result)
		}
		w.Flush()
	}
	return nil
}

// productionUpdate asks the controller to bring production to the head of
// the production branch now, and waits for the result.
func (c *client) productionUpdate(wait bool) error {
	requested := time.Now()
	var p api.Production
	if err := c.send(http.MethodPost, "/v1/production/update", nil, &p); err != nil {
		return err
	}
	fmt.Printf("Updating production to the latest %s ...\n", p.Branch)
	if !wait {
		return nil
	}
	// Compare against the server's own timestamps, not this machine's clock.
	before := time.Time{}
	if p.LastChecked != nil {
		before = *p.LastChecked
	}
	shown := false
	for {
		time.Sleep(2 * time.Second)
		if err := c.get("/v1/production", &p); err != nil {
			return err
		}
		if p.Status == api.ProductionUpdating && !shown {
			fmt.Println("  building and swapping changed services ...")
			shown = true
		}
		// Done once a check that started after our request has finished.
		if p.Status != api.ProductionUpdating && p.LastChecked != nil && p.LastChecked.After(before) {
			break
		}
		if time.Since(requested) > 30*time.Minute {
			return fmt.Errorf("timed out waiting for the production update")
		}
	}
	fmt.Println()
	return c.productionStatus()
}
