// Command branchbox-proxy is the BranchBox data plane. It runs in one of
// two modes:
//
//	branchbox-proxy sidecar   transparent per-service router (needs iptables)
//	branchbox-proxy edge      entry point that tags traffic with an env ID
//
// Configuration is read from environment variables (see README).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"branchbox/internal/proxy"
	"branchbox/internal/routing"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: branchbox-proxy sidecar|edge")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	controller := env("BB_CONTROLLER_URL", "http://branchbox-controller:8090")
	token := os.Getenv("BB_API_TOKEN")
	table := routing.NewTable()
	events := proxy.NewEventSink(controller, token)
	go events.Run(ctx)

	var err error
	switch os.Args[1] {
	case "sidecar":
		s := &proxy.Sidecar{
			Service:     mustEnv("BB_SERVICE"),
			Sandbox:     os.Getenv("BB_SANDBOX"),
			Listen:      env("BB_LISTEN", ":15001"),
			AdminListen: env("BB_ADMIN_LISTEN", ":15000"),
			PeekTimeout: 300 * time.Millisecond,
			Table:       table,
			Events:      events,
		}
		go table.Sync(ctx, controller, token, routing.Logf("[sidecar "+s.Service+"] "))
		err = s.Run(ctx)
	case "edge":
		e := &proxy.Edge{
			Listen:   env("BB_EDGE_LISTEN", ":8080"),
			Service:  mustEnv("BB_EDGE_SERVICE"),
			Upstream: mustEnv("BB_EDGE_UPSTREAM"),
			Domain:   env("BB_EDGE_DOMAIN", "localhost"),
			Table:    table,
			Events:   events,
		}
		go table.Sync(ctx, controller, token, routing.Logf("[edge] "))
		err = e.Run(ctx)
	default:
		err = fmt.Errorf("unknown mode %q (want sidecar or edge)", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s must be set", key)
	}
	return v
}
