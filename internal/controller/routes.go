package controller

import (
	"context"
	"sort"
	"sync"

	"branchbox/internal/api"
)

// RouteHub owns the authoritative route table and lets proxies long-poll
// for changes.
type RouteHub struct {
	mu       sync.Mutex
	version  uint64
	services []api.Service
	routes   map[[2]string]string // {service, env} -> address
	changed  chan struct{}        // closed (and replaced) on every change
}

// NewRouteHub returns a hub for the given project services.
func NewRouteHub(services []api.Service) *RouteHub {
	return &RouteHub{version: 1, services: services, routes: map[[2]string]string{}, changed: make(chan struct{})}
}

// Set adds or replaces the route for (service, env).
func (h *RouteHub) Set(service, env, address string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routes[[2]string{service, env}] = address
	h.bumpLocked()
}

// Remove deletes the route for (service, env); traffic falls back to the
// baseline as soon as proxies observe the new version.
func (h *RouteHub) Remove(service, env string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.routes[[2]string{service, env}]; ok {
		delete(h.routes, [2]string{service, env})
		h.bumpLocked()
	}
}

func (h *RouteHub) bumpLocked() {
	h.version++
	close(h.changed)
	h.changed = make(chan struct{})
}

// Table returns a snapshot of the current table.
func (h *RouteHub) Table() api.RouteTable {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tableLocked()
}

func (h *RouteHub) tableLocked() api.RouteTable {
	rt := api.RouteTable{Version: h.version, Services: h.services, Routes: make([]api.Route, 0, len(h.routes))}
	for k, addr := range h.routes {
		rt.Routes = append(rt.Routes, api.Route{Service: k[0], Env: k[1], Address: addr})
	}
	sort.Slice(rt.Routes, func(i, j int) bool {
		a, b := rt.Routes[i], rt.Routes[j]
		return a.Service+"/"+a.Env < b.Service+"/"+b.Env
	})
	return rt
}

// Wait blocks until the version differs from after (or ctx ends) and
// returns the table at that point.
func (h *RouteHub) Wait(ctx context.Context, after uint64) api.RouteTable {
	h.mu.Lock()
	if h.version != after {
		defer h.mu.Unlock()
		return h.tableLocked()
	}
	ch := h.changed
	h.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
	return h.Table()
}
