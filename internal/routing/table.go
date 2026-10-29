// Package routing holds the proxy-side copy of the controller's route table
// and keeps it current by long-polling the controller.
package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"branchbox/internal/api"
)

// Table is a lock-free, read-mostly view of the route table. Lookups happen
// on every proxied request; updates happen only when sandboxes change.
type Table struct {
	snap atomic.Pointer[snapshot]

	mu    sync.RWMutex
	ipMap map[string]string // "ip:port" -> service, refreshed from DNS
}

type snapshot struct {
	version  uint64
	services map[string]api.Service       // name -> service
	routes   map[string]map[string]string // service -> env -> address
}

// NewTable returns an empty table: until the first sync every request is
// served by the baseline, which is always the safe default.
func NewTable() *Table {
	t := &Table{ipMap: map[string]string{}}
	t.snap.Store(&snapshot{services: map[string]api.Service{}, routes: map[string]map[string]string{}})
	return t
}

// Lookup returns the sandbox address for (service, env), if one exists.
func (t *Table) Lookup(service, env string) (string, bool) {
	if service == "" || env == "" {
		return "", false
	}
	addr, ok := t.snap.Load().routes[service][env]
	return addr, ok
}

// Routes returns the active sandbox routes, sorted by environment.
func (t *Table) Routes() []api.Route {
	var out []api.Route
	for service, envs := range t.snap.Load().routes {
		for env, addr := range envs {
			out = append(out, api.Route{Service: service, Env: env, Address: addr})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Env != out[j].Env {
			return out[i].Env < out[j].Env
		}
		return out[i].Service < out[j].Service
	})
	return out
}

// Version returns the version of the currently loaded table.
func (t *Table) Version() uint64 { return t.snap.Load().version }

// ServiceFor identifies which project service a request is addressed to,
// first from the hostname the caller used (Host / :authority), then from
// the original destination IP:port. It returns "" for destinations that
// are not part of the project (databases, external APIs, ...).
func (t *Table) ServiceFor(authority string, dst string) string {
	s := t.snap.Load()
	host := authority
	if h, _, err := net.SplitHostPort(authority); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if _, ok := s.services[host]; ok {
		return host
	}
	// Fully-qualified forms such as "paymentservice.default.svc".
	if short, _, ok := strings.Cut(host, "."); ok {
		if _, ok := s.services[short]; ok {
			return short
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ipMap[dst]
}

func (t *Table) store(rt api.RouteTable) {
	s := &snapshot{
		version:  rt.Version,
		services: make(map[string]api.Service, len(rt.Services)),
		routes:   make(map[string]map[string]string),
	}
	for _, svc := range rt.Services {
		s.services[svc.Name] = svc
	}
	for _, r := range rt.Routes {
		if s.routes[r.Service] == nil {
			s.routes[r.Service] = map[string]string{}
		}
		s.routes[r.Service][r.Env] = r.Address
	}
	t.snap.Store(s)
}

// Sync keeps the table current until ctx is cancelled. It long-polls
// GET {controller}/v1/routes?wait=<version>, so route changes reach every
// proxy within milliseconds without constant polling.
// token, when non-empty, is sent as a bearer token to the controller.
func (t *Table) Sync(ctx context.Context, controller, token string, logf func(string, ...any)) {
	client := &http.Client{Timeout: 60 * time.Second}
	go t.refreshIPs(ctx)
	for ctx.Err() == nil {
		rt, err := fetch(ctx, client, controller, token, t.Version())
		if err != nil {
			logf("route sync failed (keeping last table, version %d): %v", t.Version(), err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if rt.Version != t.Version() || t.Version() == 0 {
			t.store(rt)
			logf("route table updated to version %d (%d sandbox routes)", rt.Version, len(rt.Routes))
		}
	}
}

func fetch(ctx context.Context, c *http.Client, controller, token string, version uint64) (api.RouteTable, error) {
	var rt api.RouteTable
	url := fmt.Sprintf("%s/v1/routes?wait=%d", strings.TrimRight(controller, "/"), version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return rt, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return rt, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rt, fmt.Errorf("controller returned %s", resp.Status)
	}
	return rt, json.NewDecoder(resp.Body).Decode(&rt)
}

// refreshIPs periodically resolves every service name so that connections
// made to a raw IP (no usable Host header) can still be attributed.
func (t *Table) refreshIPs(ctx context.Context) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		m := map[string]string{}
		for name, svc := range t.snap.Load().services {
			ips, err := net.DefaultResolver.LookupHost(ctx, name)
			if err != nil {
				continue
			}
			for _, ip := range ips {
				m[net.JoinHostPort(ip, fmt.Sprint(svc.Port))] = name
			}
		}
		t.mu.Lock()
		t.ipMap = m
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Logf is a small adapter so callers can pass log.Printf with a prefix.
func Logf(prefix string) func(string, ...any) {
	return func(format string, args ...any) { log.Printf(prefix+format, args...) }
}
