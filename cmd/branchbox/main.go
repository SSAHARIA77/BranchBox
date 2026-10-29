// Command branchbox is the developer CLI for BranchBox.
//
//	branchbox sandbox create --service paymentservice --branch fix/payment-validation
//	branchbox sandbox list
//	branchbox trace <trace-id>
//
// It talks to the controller's REST API (BRANCHBOX_CONTROLLER, default
// http://127.0.0.1:8090).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const usage = `BranchBox - deploy only what changed, test against everything else.

Usage:
  branchbox [--controller URL] [--token TOKEN] <command> [arguments]

Commands:
  status                           Controller, project and active sandbox routes
  services                         Services that can be sandboxed
  sandbox create --service S --branch B [--id ID] [--no-wait]
                                   Build S from branch B and route env ID traffic to it
  sandbox list                     All sandboxes and their status
  sandbox get ID                   Details of one sandbox
  sandbox logs ID [--component app|sidecar|build] [--tail N] [--follow]
                                   Logs of the sandboxed service, its sidecar, or its build
  sandbox delete ID... | --all     Remove sandboxes (traffic falls back to the baseline)
  trace TRACE_ID                   Every routing decision made for one request
  events [--env ID] [--limit N]    Recent routing decisions

Environment:
  BRANCHBOX_CONTROLLER   controller URL (default http://127.0.0.1:8090)
  BRANCHBOX_TOKEN        API token, required by a shared BranchBox server
`

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	controller := flag.String("controller", envOr("BRANCHBOX_CONTROLLER", "http://127.0.0.1:8090"), "controller URL")
	token := flag.String("token", os.Getenv("BRANCHBOX_TOKEN"), "API token of a shared BranchBox server")
	flag.Parse()
	c := &client{base: strings.TrimRight(*controller, "/"), token: *token}

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "status":
		err = c.status()
	case "services":
		err = c.services()
	case "sandbox", "sb":
		err = c.sandbox(args[1:])
	case "trace":
		if len(args) != 2 {
			err = fmt.Errorf("usage: branchbox trace TRACE_ID")
			break
		}
		err = c.trace(args[1])
	case "events":
		err = c.events(args[1:])
	case "help", "-h", "--help":
		flag.Usage()
	default:
		err = fmt.Errorf("unknown command %q (run 'branchbox help')", args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func (c *client) sandbox(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: branchbox sandbox create|list|get|logs|delete")
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("sandbox create", flag.ExitOnError)
		service := fs.String("service", "", "service to sandbox (see 'branchbox services')")
		branch := fs.String("branch", "", "Git branch to build it from")
		id := fs.String("id", "", "sandbox / environment ID (generated if empty)")
		noWait := fs.Bool("no-wait", false, "return immediately instead of waiting until ready")
		parseInterleaved(fs, args[1:])
		if *service == "" || *branch == "" {
			return fmt.Errorf("--service and --branch are required")
		}
		return c.createSandbox(*service, *branch, *id, !*noWait)
	case "list", "ls":
		return c.listSandboxes()
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: branchbox sandbox get ID")
		}
		return c.getSandbox(args[1])
	case "logs":
		fs := flag.NewFlagSet("sandbox logs", flag.ExitOnError)
		component := fs.String("component", "app", "app, sidecar or build")
		tail := fs.Int("tail", 100, "number of lines from the end")
		follow := fs.Bool("follow", false, "stream new log lines")
		pos := parseInterleaved(fs, args[1:])
		if len(pos) != 1 {
			return fmt.Errorf("usage: branchbox sandbox logs ID [--component app|sidecar|build] [--tail N] [--follow]")
		}
		return c.logs(pos[0], *component, *tail, *follow)
	case "delete", "rm":
		fs := flag.NewFlagSet("sandbox delete", flag.ExitOnError)
		all := fs.Bool("all", false, "delete every sandbox")
		ids := parseInterleaved(fs, args[1:])
		if !*all && len(ids) == 0 {
			return fmt.Errorf("usage: branchbox sandbox delete ID... | --all")
		}
		return c.deleteSandboxes(ids, *all)
	}
	return fmt.Errorf("unknown sandbox command %q", args[0])
}

// parseInterleaved lets flags appear before or after positional arguments
// ("logs sb-1 --follow" and "logs --follow sb-1" both work).
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		_ = fs.Parse(args)
		rest := fs.Args()
		if len(rest) == 0 {
			return positional
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
