package controller

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"branchbox/internal/api"
	"branchbox/internal/project"
)

// Server exposes the controller's REST API, used by the CLI today and by
// the developer dashboard in Major Project-II.
type Server struct {
	Config  *project.Config
	Manager *Manager
	Routes  *RouteHub
	Events  *EventLog
	Info    api.Info
	// Token, when set, must accompany every request except /healthz as
	// "Authorization: Bearer <token>". The controller drives Docker, so it
	// must never be reachable from a network without one.
	Token string
}

// Handler returns the API router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.Info) })
	mux.HandleFunc("GET /v1/project", s.getProject)
	mux.HandleFunc("GET /v1/routes", s.getRoutes)
	mux.HandleFunc("GET /v1/sandboxes", s.listSandboxes)
	mux.HandleFunc("POST /v1/sandboxes", s.createSandbox)
	mux.HandleFunc("GET /v1/sandboxes/{id}", s.getSandbox)
	mux.HandleFunc("DELETE /v1/sandboxes/{id}", s.deleteSandbox)
	mux.HandleFunc("GET /v1/sandboxes/{id}/logs", s.sandboxLogs)
	mux.HandleFunc("POST /v1/events", s.postEvents)
	mux.HandleFunc("GET /v1/events", s.getEvents)
	mux.HandleFunc("GET /v1/traces/{trace}", s.getTrace)
	return s.authenticate(mux)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	if s.Token == "" {
		return next
	}
	want := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if r.URL.Path != "/healthz" && subtle.ConstantTimeCompare(got, want) != 1 {
			writeError(w, http.StatusUnauthorized, errors.New("missing or invalid API token (set BRANCHBOX_TOKEN)"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) getProject(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Config)
}

// getRoutes long-polls when ?wait=<version> is given: it returns as soon
// as the table version differs, or after 25 seconds.
func (s *Server) getRoutes(w http.ResponseWriter, r *http.Request) {
	wait, err := strconv.ParseUint(r.URL.Query().Get("wait"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusOK, s.Routes.Table())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.Routes.Wait(ctx, wait))
}

func (s *Server) listSandboxes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Manager.List())
}

func (s *Server) createSandbox(w http.ResponseWriter, r *http.Request) {
	var req api.CreateSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sb, err := s.Manager.Create(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusAccepted, sb)
}

func (s *Server) getSandbox(w http.ResponseWriter, r *http.Request) {
	sb, err := s.Manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, sb)
}

func (s *Server) deleteSandbox(w http.ResponseWriter, r *http.Request) {
	err := s.Manager.Delete(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// sandboxLogs streams ?component=app|sidecar|build logs as plain text.
func (s *Server) sandboxLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	component := q.Get("component")
	if component == "" {
		component = "app"
	}
	tail, err := strconv.Atoi(q.Get("tail"))
	if err != nil {
		tail = 200
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fw := &flushWriter{w: w, rc: http.NewResponseController(w)}
	if err := s.Manager.Logs(r.Context(), r.PathValue("id"), component, tail, q.Get("follow") == "1", fw); err != nil && r.Context().Err() == nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		fw.Write([]byte("\n[branchbox] " + err.Error() + "\n"))
	}
}

func (s *Server) postEvents(w http.ResponseWriter, r *http.Request) {
	var events []api.Event
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.Events.Add(events)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	writeJSON(w, http.StatusOK, s.Events.Query(Filter{TraceID: q.Get("trace"), Env: q.Get("env"), Limit: limit}))
}

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Events.Query(Filter{TraceID: r.PathValue("trace")}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// flushWriter flushes after every write so followed logs arrive live.
type flushWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (f *flushWriter) Write(b []byte) (int, error) {
	n, err := f.w.Write(b)
	_ = f.rc.Flush()
	return n, err
}
