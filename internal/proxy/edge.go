package proxy

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"branchbox/internal/api"
	"branchbox/internal/envid"
	"branchbox/internal/routing"
)

const (
	envCookie     = "branchbox-env"
	controlPrefix = "/__branchbox"
)

// Edge is the entry point of the application. It decides which
// environment an incoming request belongs to, tags it with that
// environment ID (W3C baggage), starts a trace if there is none, and
// forwards it to the entry service — or to that service's sandbox.
//
// The environment is taken, in order, from:
//  1. the hostname: http://sb-42.localhost:8080 selects sandbox "sb-42"
//     (needs wildcard DNS for remote hosts);
//  2. an existing baggage / X-Branchbox-Env header (curl, API tester);
//  3. the "branchbox-env" cookie, set by visiting /__branchbox/env/<id>.
//     This works from any browser on any network with no DNS setup, and
//     keeps each developer's browser in their own sandbox.
type Edge struct {
	Listen   string
	Service  string // entry service name, e.g. "frontend"
	Upstream string // baseline address of the entry service
	Domain   string // base domain for sandbox hostnames, e.g. "localhost"
	Table    *routing.Table
	Events   *EventSink
}

// Run serves until ctx is cancelled.
func (e *Edge) Run(ctx context.Context) error {
	fwd := newForwarder("edge", e.Events)
	fwd.modifyResponse = e.addBanner
	srv := &http.Server{
		Addr:     e.Listen,
		Handler:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { e.route(w, r, fwd) }),
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Printf("[edge] listening on %s, entry service %q (baseline %s), sandbox hosts *.%s",
		e.Listen, e.Service, e.Upstream, e.Domain)
	if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (e *Edge) route(w http.ResponseWriter, r *http.Request, fwd *forwarder) {
	if r.URL.Path == controlPrefix || strings.HasPrefix(r.URL.Path, controlPrefix+"/") {
		e.control(w, r)
		return
	}
	env := e.envFromHost(r.Host)
	if env == "" {
		env = envid.FromHeaders(r.Header)
	}
	if env == "" {
		if c, err := r.Cookie(envCookie); err == nil && envid.Valid(c.Value) {
			env = c.Value
		}
	}
	if env != "" {
		envid.Inject(r.Header, env)
		w.Header().Set(envid.Header, env)
	}
	traceID := envid.EnsureTraceparent(r.Header)
	w.Header().Set("X-Branchbox-Trace", traceID)

	h := &hop{
		source:   "edge",
		dest:     e.Service,
		env:      env,
		traceID:  traceID,
		decision: api.DecisionBaseline,
		target:   e.Upstream,
	}
	if addr, ok := e.Table.Lookup(e.Service, env); ok {
		h.decision, h.target = api.DecisionSandbox, addr
	}
	fwd.serve(w, r, h)
}

// control serves BranchBox's own pages under /__branchbox:
//
//	/__branchbox           pick an environment
//	/__branchbox/env/<id>  use sandbox <id> in this browser
//	/__branchbox/exit      back to the baseline
func (e *Edge) control(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == controlPrefix:
		e.picker(w, r)
	case path == controlPrefix+"/exit":
		http.SetCookie(w, &http.Cookie{Name: envCookie, Value: "", Path: "/", MaxAge: -1})
		http.Redirect(w, r, "/", http.StatusFound)
	case strings.HasPrefix(path, controlPrefix+"/env/"):
		id := strings.TrimPrefix(path, controlPrefix+"/env/")
		if !envid.Valid(id) {
			http.Error(w, "invalid environment ID", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: envCookie, Value: id, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, MaxAge: 12 * 3600})
		http.Redirect(w, r, "/", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func (e *Edge) picker(w http.ResponseWriter, r *http.Request) {
	current := ""
	if c, err := r.Cookie(envCookie); err == nil {
		current = c.Value
	}
	byEnv := map[string][]string{}
	var order []string
	for _, rt := range e.Table.Routes() {
		if _, seen := byEnv[rt.Env]; !seen {
			order = append(order, rt.Env)
		}
		byEnv[rt.Env] = append(byEnv[rt.Env], rt.Service)
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>BranchBox environments</title>
<style>body{font:16px system-ui,sans-serif;max-width:640px;margin:40px auto;padding:0 16px;color:#1d2b36}
a.env{display:block;padding:14px 16px;margin:8px 0;border:1px solid #c8d3dc;border-radius:8px;text-decoration:none;color:inherit}
a.env:hover{border-color:#1b6b4a}.on{border-color:#1b6b4a!important;background:#eef7f2}small{color:#5b6b78}</style>
<h1>BranchBox</h1><p>Choose which environment this browser uses. Other browsers are not affected.</p>`)
	cls := ""
	if current == "" {
		cls = " on"
	}
	fmt.Fprintf(&b, `<a class="env%s" href="%s/exit"><b>Production (baseline)</b><br><small>every service from the stable deployment</small></a>`, cls, controlPrefix)
	for _, env := range order {
		cls = ""
		if env == current {
			cls = " on"
		}
		fmt.Fprintf(&b, `<a class="env%s" href="%s/env/%s"><b>%s</b><br><small>sandboxed: %s · everything else from production</small></a>`,
			cls, controlPrefix, html.EscapeString(env), html.EscapeString(env), html.EscapeString(strings.Join(byEnv[env], ", ")))
	}
	if len(order) == 0 {
		b.WriteString(`<p><small>No sandboxes yet. Create one with <code>branchbox sandbox create</code>.</small></p>`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, b.String())
}

// addBanner marks HTML pages served for a tagged request, so a developer
// can always see which environment they are looking at.
func (e *Edge) addBanner(resp *http.Response) error {
	h, _ := resp.Request.Context().Value(hopKey).(*hop)
	if h == nil || h.env == "" ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	var sandboxed []string
	for _, rt := range e.Table.Routes() {
		if rt.Env == h.env {
			sandboxed = append(sandboxed, rt.Service)
		}
	}
	what := "no sandbox with this ID — every service from production"
	if len(sandboxed) > 0 {
		what = "sandboxed: " + strings.Join(sandboxed, ", ") + " · everything else from production"
	}
	banner := fmt.Sprintf(`<div style="position:fixed;left:0;right:0;bottom:0;z-index:99999;background:#1b6b4a;color:#fff;`+
		`font:14px system-ui,sans-serif;padding:8px 16px;text-align:center">BranchBox environment <b>%s</b> &mdash; %s &middot; `+
		`<a href="%s" style="color:#fff">switch</a> &middot; <a href="%s/exit" style="color:#fff">back to production</a></div>`,
		html.EscapeString(h.env), html.EscapeString(what), controlPrefix, controlPrefix)
	if i := bytes.LastIndex(body, []byte("</body>")); i >= 0 {
		body = append(body[:i:i], append([]byte(banner), body[i:]...)...)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

// envFromHost maps "<id>.<domain>[:port]" to "<id>".
func (e *Edge) envFromHost(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	id, ok := strings.CutSuffix(strings.ToLower(host), "."+e.Domain)
	if !ok || !envid.Valid(id) {
		return ""
	}
	return id
}
