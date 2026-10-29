package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"branchbox/internal/api"
	"branchbox/internal/envid"
	"branchbox/internal/routing"
)

// Sidecar transparently intercepts every outbound TCP connection of the
// service whose network namespace it shares (iptables REDIRECT, see
// deploy/sidecar-entrypoint.sh). HTTP/1.1 and HTTP/2 (gRPC) connections
// are routed per request; anything else (Redis, databases, ...) is relayed
// unchanged to its original destination.
type Sidecar struct {
	Service     string // the service this sidecar is attached to
	Sandbox     string // the sandbox ID, or "" when attached to the baseline
	Listen      string // where iptables redirects outbound traffic
	AdminListen string // readiness and debug endpoints
	PeekTimeout time.Duration
	Table       *routing.Table
	Events      *EventSink
}

// Run serves until ctx is cancelled.
func (s *Sidecar) Run(ctx context.Context) error {
	name := "sidecar " + s.Service
	if s.Sandbox != "" {
		name += "@" + s.Sandbox
	}
	fwd := newForwarder(name, s.Events)

	ln, err := net.Listen("tcp", s.Listen)
	if err != nil {
		return err
	}
	httpConns := newConnQueue(ln.Addr())
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.route(w, r, fwd) }),
		Protocols: protocols,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if ic, ok := c.(*interceptedConn); ok {
				return context.WithValue(ctx, origDstKey, ic.dst)
			}
			return ctx
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = srv.Serve(httpConns) }()
	go s.serveAdmin()
	go func() {
		<-ctx.Done()
		ln.Close()
		srv.Close()
	}()

	log.Printf("[%s] intercepting outbound traffic on %s", name, s.Listen)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go s.dispatch(c.(*net.TCPConn), httpConns, name)
	}
}

// route makes the per-request decision: sandbox if one exists for
// (destination service, environment ID), otherwise the original
// destination the application dialled.
func (s *Sidecar) route(w http.ResponseWriter, r *http.Request, fwd *forwarder) {
	dst, _ := r.Context().Value(origDstKey).(netip.AddrPort)
	h := &hop{
		source:        s.Service,
		sourceSandbox: s.Sandbox,
		dest:          s.Table.ServiceFor(r.Host, dst.String()),
		env:           envid.FromHeaders(r.Header),
		traceID:       envid.TraceID(r.Header),
		decision:      api.DecisionBaseline,
		target:        dst.String(),
		http2:         r.ProtoMajor == 2,
	}
	if addr, ok := s.Table.Lookup(h.dest, h.env); ok {
		h.decision, h.target = api.DecisionSandbox, addr
	}
	if h.dest == "" {
		h.dest = r.Host // not a project service; always passed through
	}
	fwd.serve(w, r, h)
}

// dispatch recovers the original destination and sniffs the protocol.
func (s *Sidecar) dispatch(c *net.TCPConn, httpConns *connQueue, name string) {
	dst, err := originalDst(c)
	if err != nil {
		log.Printf("[%s] dropping connection from %s: %v", name, c.RemoteAddr(), err)
		c.Close()
		return
	}
	br := bufio.NewReaderSize(c, 8192)
	// Client-speaks-first protocols (HTTP, gRPC, Redis) send bytes at once;
	// for server-speaks-first ones the peek times out and we relay raw.
	_ = c.SetReadDeadline(time.Now().Add(s.PeekTimeout))
	head, peekErr := br.Peek(4)
	_ = c.SetReadDeadline(time.Time{})

	ic := &interceptedConn{Conn: c, tcp: c, r: br, dst: dst}
	if peekErr == nil && looksLikeHTTP(head) {
		httpConns.push(ic)
		return
	}
	relayTCP(ic, dst)
}

func looksLikeHTTP(b []byte) bool {
	switch string(b) {
	case "PRI ", // HTTP/2 connection preface (gRPC)
		"GET ", "POST", "PUT ", "HEAD", "DELE", "PATC", "OPTI", "TRAC", "CONN":
		return true
	}
	return false
}

// relayTCP passes a non-HTTP connection through to its original
// destination untouched.
func relayTCP(c *interceptedConn, dst netip.AddrPort) {
	defer c.Close()
	up, err := net.DialTimeout("tcp", dst.String(), 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, c)
		_ = up.(*net.TCPConn).CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(c, up)
		_ = c.tcp.CloseWrite()
	}()
	wg.Wait()
}

func (s *Sidecar) serveAdmin() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "ready (route table version %d)\n", s.Table.Version())
	})
	if err := http.ListenAndServe(s.AdminListen, mux); err != nil {
		log.Printf("admin listener stopped: %v", err)
	}
}

// interceptedConn replays the bytes consumed while sniffing. It embeds
// the net.Conn interface (not *net.TCPConn) on purpose: TCPConn's
// WriteTo/ReadFrom fast paths would bypass the buffered reader.
type interceptedConn struct {
	net.Conn
	tcp *net.TCPConn
	r   *bufio.Reader
	dst netip.AddrPort
}

func (c *interceptedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// connQueue is a net.Listener fed by the dispatcher, so http.Server only
// ever sees connections already identified as HTTP.
type connQueue struct {
	ch   chan net.Conn
	addr net.Addr
	done chan struct{}
	once sync.Once
}

func newConnQueue(addr net.Addr) *connQueue {
	return &connQueue{ch: make(chan net.Conn), addr: addr, done: make(chan struct{})}
}

func (q *connQueue) push(c net.Conn) {
	select {
	case q.ch <- c:
	case <-q.done:
		c.Close()
	}
}

func (q *connQueue) Accept() (net.Conn, error) {
	select {
	case c := <-q.ch:
		return c, nil
	case <-q.done:
		return nil, net.ErrClosed
	}
}

func (q *connQueue) Close() error {
	q.once.Do(func() { close(q.done) })
	return nil
}

func (q *connQueue) Addr() net.Addr { return q.addr }
