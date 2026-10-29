// Package proxy implements the BranchBox data plane: the transparent
// sidecar attached to every service, and the edge that tags incoming
// traffic. Both make the same per-request decision — "does a sandbox exist
// for this (destination service, environment ID)?" — and forward the
// request to the sandbox or to the baseline accordingly.
package proxy

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"branchbox/internal/api"
)

type ctxKey int

const (
	origDstKey ctxKey = iota
	hopKey
)

// hop is the routing decision for one request, made before forwarding.
type hop struct {
	source        string
	sourceSandbox string
	dest          string
	env           string
	traceID       string
	decision      string
	target        string // host:port actually dialled
	http2         bool
}

// forwarder is an L7 reverse proxy that sends each request to hop.target.
// Routing happens per request rather than per connection, which matters
// for gRPC: a single long-lived HTTP/2 connection carries requests for many
// different environment IDs.
type forwarder struct {
	rp     *httputil.ReverseProxy
	events *EventSink
	name   string
	// modifyResponse, if set, may rewrite upstream responses (the edge uses
	// it to show which environment a page was served from).
	modifyResponse func(*http.Response) error
}

func newForwarder(name string, events *EventSink) *forwarder {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	h1 := &http.Transport{
		DialContext:         dialer.DialContext,
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	h2c := new(http.Protocols)
	h2c.SetUnencryptedHTTP2(true)
	h2 := &http.Transport{
		DialContext:        dialer.DialContext,
		Protocols:          h2c,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: true,
	}
	f := &forwarder{events: events, name: name}
	f.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			h := pr.In.Context().Value(hopKey).(*hop)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = h.target
			// Keep the authority the application used (e.g.
			// "paymentservice:50051") so the upstream sees an unchanged
			// request regardless of where it was routed.
			pr.Out.Host = pr.In.Host
		},
		Transport: protocolSwitch{h1: h1, h2: h2},
		ModifyResponse: func(resp *http.Response) error {
			if f.modifyResponse != nil {
				return f.modifyResponse(resp)
			}
			return nil
		},
		FlushInterval: -1, // stream responses (gRPC, server-sent events)
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h := r.Context().Value(hopKey).(*hop)
			log.Printf("[%s] upstream error: %s -> %s (%s): %v", f.name, h.source, h.dest, h.target, err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return f
}

// protocolSwitch speaks to the upstream in the same protocol the caller
// used: HTTP/1.1 stays HTTP/1.1, and prior-knowledge HTTP/2 (gRPC) stays
// HTTP/2 without TLS.
type protocolSwitch struct{ h1, h2 http.RoundTripper }

func (p protocolSwitch) RoundTrip(r *http.Request) (*http.Response, error) {
	if h, ok := r.Context().Value(hopKey).(*hop); ok && h.http2 {
		return p.h2.RoundTrip(r)
	}
	return p.h1.RoundTrip(r)
}

// serve forwards r according to h and records the outcome.
func (f *forwarder) serve(w http.ResponseWriter, r *http.Request, h *hop) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	f.rp.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), hopKey, h)))

	ev := api.Event{
		Time:          start,
		TraceID:       h.traceID,
		Env:           h.env,
		Source:        h.source,
		SourceSandbox: h.sourceSandbox,
		Dest:          h.dest,
		Decision:      h.decision,
		Target:        h.target,
		Protocol:      protocolName(r),
		Method:        r.Method,
		Path:          r.URL.Path,
		Status:        rec.status,
		GRPCStatus:    grpcStatus(rec.Header()),
		DurationMs:    float64(time.Since(start).Microseconds()) / 1000,
	}
	if f.events != nil {
		f.events.Record(ev)
	}
	env := ev.Env
	if env == "" {
		env = "-"
	}
	log.Printf("[%s] %s %s -> %s %s (%s) status=%d%s env=%s trace=%s %.1fms",
		f.name, ev.Method, ev.Path, ev.Dest, strings.ToUpper(ev.Decision), ev.Target,
		ev.Status, grpcSuffix(ev.GRPCStatus), env, shortTrace(ev.TraceID), ev.DurationMs)
}

func protocolName(r *http.Request) string {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		return "grpc"
	}
	if r.ProtoMajor == 2 {
		return "h2c"
	}
	return "http/1.1"
}

// grpcStatus finds the grpc-status code wherever ReverseProxy left it:
// in the headers (trailers-only responses), as an announced trailer, or
// as an unannounced trailer stored under http.TrailerPrefix.
func grpcStatus(h http.Header) string {
	if v := h.Get("Grpc-Status"); v != "" {
		return v
	}
	if v := h[http.TrailerPrefix+"Grpc-Status"]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func grpcSuffix(s string) string {
	if s == "" {
		return ""
	}
	return " grpc-status=" + s
}

func shortTrace(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	if id == "" {
		return "-"
	}
	return id
}

// statusRecorder captures the response status while staying transparent
// to http.ResponseController (flushing and trailers keep working).
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader && code >= 200 {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() { _ = http.NewResponseController(s.ResponseWriter).Flush() }

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
