package controller

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"branchbox/internal/api"
	"branchbox/internal/envid"
	"branchbox/internal/gitsrc"
	"branchbox/internal/project"
	"branchbox/internal/runtime"
)

// ErrNotFound is returned for unknown sandbox IDs.
var ErrNotFound = errors.New("sandbox not found")

// Manager owns the sandbox lifecycle:
//
//	pending -> building (fetch branch, export service source, docker build)
//	        -> starting (sidecar + service containers, wait until listening)
//	        -> ready    (route published: tagged traffic now reaches it)
//
// and, on delete, unpublishes the route first so traffic falls back to the
// baseline before any container is removed.
type Manager struct {
	cfg     *project.Config
	repo    *gitsrc.Repo
	rt      runtime.Runtime
	routes  *RouteHub
	store   *Store
	dataDir string

	mu        sync.Mutex
	sandboxes map[string]*api.Sandbox
	cancels   map[string]context.CancelFunc
}

// NewManager wires the lifecycle dependencies together.
func NewManager(cfg *project.Config, repo *gitsrc.Repo, rt runtime.Runtime, routes *RouteHub, store *Store, dataDir string) *Manager {
	return &Manager{
		cfg: cfg, repo: repo, rt: rt, routes: routes, store: store, dataDir: dataDir,
		sandboxes: map[string]*api.Sandbox{},
		cancels:   map[string]context.CancelFunc{},
	}
}

// Reconcile restores state after a controller restart: running sandboxes
// get their routes back; anything interrupted mid-provisioning is failed.
func (m *Manager) Reconcile(ctx context.Context) error {
	list, err := m.store.Load()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range list {
		sb := list[i]
		switch {
		case sb.Status == api.StatusReady && m.rt.Alive(ctx, m.spec(&sb)):
			m.routes.Set(sb.Service, sb.ID, sb.Address)
			log.Printf("restored sandbox %s (%s @ %s)", sb.ID, sb.Service, sb.Branch)
		case sb.Status == api.StatusReady:
			sb.Status, sb.Error = api.StatusFailed, "containers are no longer running"
		case sb.Status != api.StatusFailed:
			sb.Status, sb.Error = api.StatusFailed, "controller restarted during provisioning"
		}
		m.sandboxes[sb.ID] = &sb
	}
	return m.saveLocked()
}

// Create validates the request and starts provisioning asynchronously.
func (m *Manager) Create(req api.CreateSandboxRequest) (api.Sandbox, error) {
	svc, ok := m.cfg.Service(req.Service)
	if !ok {
		return api.Sandbox{}, fmt.Errorf("unknown service %q", req.Service)
	}
	if req.Branch == "" {
		return api.Sandbox{}, fmt.Errorf("branch is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.ID == "" {
		req.ID = m.newIDLocked()
	}
	if !envid.Valid(req.ID) {
		return api.Sandbox{}, fmt.Errorf("invalid sandbox id %q: use 1-30 lowercase letters, digits and hyphens", req.ID)
	}
	if _, exists := m.sandboxes[req.ID]; exists {
		return api.Sandbox{}, fmt.Errorf("sandbox %q already exists", req.ID)
	}
	active := 0
	for _, sb := range m.sandboxes {
		if sb.Status != api.StatusFailed {
			active++
		}
	}
	if active >= m.cfg.Limits.MaxSandboxes {
		return api.Sandbox{}, fmt.Errorf("sandbox limit reached (%d active); delete one first", active)
	}
	sb := &api.Sandbox{ID: req.ID, Service: svc.Name, Branch: req.Branch, Status: api.StatusPending, CreatedAt: time.Now().UTC()}
	m.sandboxes[sb.ID] = sb
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	m.cancels[sb.ID] = cancel
	_ = m.saveLocked()
	go m.provision(ctx, sb.ID, svc)
	return *sb, nil
}

func (m *Manager) provision(ctx context.Context, id string, svc project.Service) {
	defer m.finishProvisioning(id)
	workDir := filepath.Join(m.dataDir, "work", id)
	_ = os.RemoveAll(workDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		m.fail(id, err)
		return
	}
	buildLog, err := os.Create(filepath.Join(workDir, "build.log"))
	if err != nil {
		m.fail(id, err)
		return
	}
	defer buildLog.Close()
	logf := func(format string, args ...any) {
		fmt.Fprintf(buildLog, "[branchbox] "+format+"\n", args...)
		log.Printf("sandbox %s: "+format, append([]any{id}, args...)...)
	}

	sb := m.update(id, func(sb *api.Sandbox) { sb.Status = api.StatusBuilding })
	logf("fetching %s", m.repo.URL)
	if err := m.repo.Fetch(ctx); err != nil {
		m.fail(id, err)
		return
	}
	commit, err := m.repo.Resolve(ctx, sb.Branch)
	if err != nil {
		m.fail(id, err)
		return
	}
	image := fmt.Sprintf("branchbox/%s-%s:%s", m.cfg.Project, svc.Name, id)
	m.update(id, func(sb *api.Sandbox) { sb.Commit, sb.Image = commit, image })
	logf("branch %s is at commit %s; exporting %s", sb.Branch, commit[:12], svc.Build.Context)

	srcDir := filepath.Join(workDir, "src")
	if err := m.repo.Export(ctx, commit, svc.Build.Context, srcDir); err != nil {
		m.fail(id, err)
		return
	}
	logf("building image %s", image)
	if err := m.rt.Build(ctx, runtime.BuildSpec{
		ContextDir: srcDir,
		Dockerfile: svc.Build.Dockerfile,
		Tag:        image,
		Labels:     map[string]string{"branchbox.sandbox": id, "branchbox.commit": commit},
	}, buildLog); err != nil {
		m.fail(id, err)
		return
	}
	_ = os.RemoveAll(srcDir)

	sb = m.update(id, func(sb *api.Sandbox) { sb.Status = api.StatusStarting })
	logf("starting sandbox containers")
	addr, err := m.rt.Deploy(ctx, m.spec(&sb))
	if err != nil {
		_ = m.rt.Destroy(context.Background(), m.spec(&sb))
		m.fail(id, err)
		return
	}
	now := time.Now().UTC()
	sb = m.update(id, func(sb *api.Sandbox) {
		sb.Status, sb.Address, sb.ReadyAt = api.StatusReady, addr, &now
		// Published under the manager lock so a concurrent Delete can never
		// leave a stale route behind.
		m.routes.Set(sb.Service, sb.ID, addr)
	})
	if sb.Status != api.StatusReady {
		return // deleted while starting
	}
	logf("ready at %s after %.1fs; route %s/%s published", addr, sb.SetupSeconds(), sb.Service, sb.ID)
}

func (m *Manager) finishProvisioning(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[id]; ok {
		cancel()
		delete(m.cancels, id)
	}
}

// Delete unpublishes the route, then removes the sandbox's containers,
// image and working files. It also cancels an in-progress build.
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	sb, ok := m.sandboxes[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if cancel, ok := m.cancels[id]; ok {
		cancel()
	}
	sb.Status = api.StatusDeleting
	snapshot := *sb
	m.mu.Unlock()

	m.routes.Remove(snapshot.Service, snapshot.ID)
	if err := m.rt.Destroy(ctx, m.spec(&snapshot)); err != nil {
		return err
	}
	if snapshot.Image != "" {
		m.rt.RemoveImage(ctx, snapshot.Image)
	}
	_ = os.RemoveAll(filepath.Join(m.dataDir, "work", id))

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sandboxes, id)
	return m.saveLocked()
}

// Get returns one sandbox.
func (m *Manager) Get(id string) (api.Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sandboxes[id]
	if !ok {
		return api.Sandbox{}, ErrNotFound
	}
	return *sb, nil
}

// List returns all sandboxes, oldest first.
func (m *Manager) List() []api.Sandbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]api.Sandbox, 0, len(m.sandboxes))
	for _, sb := range m.sandboxes {
		out = append(out, *sb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Logs streams a sandbox's build log, service log or sidecar log.
func (m *Manager) Logs(ctx context.Context, id, component string, tail int, follow bool, w io.Writer) error {
	sb, err := m.Get(id)
	if err != nil {
		return err
	}
	if component == "build" {
		f, err := os.Open(filepath.Join(m.dataDir, "work", id, "build.log"))
		if err != nil {
			return fmt.Errorf("no build log for %s", id)
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}
	return m.rt.Logs(ctx, m.spec(&sb), runtime.Component(component), tail, follow, w)
}

func (m *Manager) spec(sb *api.Sandbox) runtime.SandboxSpec {
	svc, _ := m.cfg.Service(sb.Service)
	return runtime.SandboxSpec{ID: sb.ID, Service: sb.Service, Port: svc.Port, Image: sb.Image}
}

// update applies fn to the stored record, persists it, and returns a copy.
func (m *Manager) update(id string, fn func(*api.Sandbox)) api.Sandbox {
	m.mu.Lock()
	defer m.mu.Unlock()
	sb, ok := m.sandboxes[id]
	if !ok {
		return api.Sandbox{ID: id}
	}
	if sb.Status != api.StatusDeleting {
		fn(sb)
	}
	_ = m.saveLocked()
	return *sb
}

func (m *Manager) fail(id string, err error) {
	if errors.Is(err, context.Canceled) {
		return // deleted while provisioning
	}
	log.Printf("sandbox %s failed: %v", id, err)
	m.update(id, func(sb *api.Sandbox) { sb.Status, sb.Error = api.StatusFailed, err.Error() })
}

func (m *Manager) saveLocked() error {
	if err := m.store.Save(m.sandboxes); err != nil {
		log.Printf("saving state: %v", err)
		return err
	}
	return nil
}

// newIDLocked generates a short, readable, unused ID such as "sb-k3x9q".
func (m *Manager) newIDLocked() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	for {
		b := make([]byte, 5)
		_, _ = rand.Read(b)
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		id := "sb-" + string(b)
		if _, taken := m.sandboxes[id]; !taken {
			return id
		}
	}
}
