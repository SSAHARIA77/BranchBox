// Command branchbox-controller is the BranchBox control plane: it builds
// and runs sandboxes, owns the route table the proxies follow, and
// collects their routing events.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"branchbox/internal/api"
	"branchbox/internal/controller"
	"branchbox/internal/gitsrc"
	"branchbox/internal/project"
	"branchbox/internal/runtime"
)

func main() {
	log.SetFlags(log.Ltime)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := project.Load(env("BB_PROJECT_FILE", "/etc/branchbox/branchbox.yaml"))
	if err != nil {
		log.Fatal(err)
	}
	dataDir := env("BB_DATA_DIR", "/data")
	// Deployment-specific overrides, so the same branchbox.yaml works on a
	// laptop (local repository) and on a shared server (GitHub repository).
	if url := os.Getenv("BB_REPOSITORY_URL"); url != "" {
		cfg.Repository.URL = url
	}
	token := os.Getenv("BB_API_TOKEN")

	rt := &runtime.Docker{
		ComposeProject: cfg.Runtime.ComposeProject,
		Network:        cfg.Runtime.Network,
		ProxyImage:     env("BB_PROXY_IMAGE", "branchbox/proxy:dev"),
		ControllerURL:  env("BB_ADVERTISE_URL", "http://branchbox-controller:8090"),
		Token:          token,
	}
	routes := controller.NewRouteHub(cfg.APIServices())
	repo := &gitsrc.Repo{URL: cfg.Repository.URL, Dir: filepath.Join(dataDir, "repos", cfg.Project+".git")}
	mgr := controller.NewManager(cfg, repo, rt, routes, controller.NewStore(filepath.Join(dataDir, "state.json")), dataDir)
	if err := mgr.Reconcile(ctx); err != nil {
		log.Fatalf("reconcile: %v", err)
	}

	srv := &http.Server{
		Addr: env("BB_LISTEN", ":8090"),
		Handler: (&controller.Server{
			Config:  cfg,
			Manager: mgr,
			Routes:  routes,
			Events:  controller.NewEventLog(20000),
			Token:   token,
			Info: api.Info{
				Project:      cfg.Project,
				EntryService: cfg.Entry.Service,
				EdgeDomain:   env("BB_EDGE_DOMAIN", "localhost"),
				EdgePort:     env("BB_EDGE_PORT", "8080"),
				Repository:   cfg.Repository.URL,
			},
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	auth := "NOT set (local use only)"
	if token != "" {
		auth = "required"
	}
	log.Printf("branchbox controller for project %q (%d services) on %s; repository %s; API token %s",
		cfg.Project, len(cfg.Services), srv.Addr, cfg.Repository.URL, auth)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
