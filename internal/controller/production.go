package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"branchbox/internal/api"
	"branchbox/internal/gitsrc"
	"branchbox/internal/project"
	"branchbox/internal/runtime"
)

// Production keeps the baseline in step with the production branch.
//
// For every service it compares the Git tree of the service's directory at
// the branch head with the tree at repository.baseRef (the release the
// prebuilt baseline images were made from):
//
//   - same tree    -> the prebuilt upstream image is exactly this code; keep it
//   - different    -> build the service from the branch and swap it in
//
// Images built from the branch are tagged by tree hash, so a commit that
// does not touch a service never rebuilds or restarts it. The check runs at
// startup, every pollSeconds when autoUpdate is on, and on demand.
type Production struct {
	cfg     *project.Config
	repo    *gitsrc.Repo
	rt      runtime.Runtime
	dataDir string
	branch  string
	auto    bool
	poll    time.Duration

	syncMu  sync.Mutex // one synchronisation at a time
	trigger chan string

	mu     sync.Mutex // guards state and failed
	state  api.Production
	failed map[string]bool // "service@tree" builds that failed; retried only manually
}

// NewProduction returns a production manager; Run starts it.
func NewProduction(cfg *project.Config, repo *gitsrc.Repo, rt runtime.Runtime, dataDir, branch string, auto bool, poll time.Duration) *Production {
	p := &Production{
		cfg: cfg, repo: repo, rt: rt, dataDir: dataDir,
		branch: branch, auto: auto, poll: poll,
		trigger: make(chan string, 1),
		failed:  map[string]bool{},
		state: api.Production{
			Branch: branch, AutoUpdate: auto, PollSeconds: int(poll.Seconds()),
			Status: api.ProductionStarting,
		},
	}
	if raw, err := os.ReadFile(p.statePath()); err == nil {
		var saved api.Production
		if json.Unmarshal(raw, &saved) == nil {
			p.state.History = saved.History
			if saved.Branch == branch {
				p.state.Commit, p.state.Subject = saved.Commit, saved.Subject
			}
		}
	}
	return p
}

// Run synchronises once at startup and then whenever triggered or, with
// auto-update, every poll interval.
func (p *Production) Run(ctx context.Context) {
	p.waitForBaseline(ctx, 3*time.Minute)
	p.sync(ctx, "startup")
	var tick <-chan time.Time
	if p.auto {
		t := time.NewTicker(p.poll)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			p.sync(ctx, "auto")
		case reason := <-p.trigger:
			p.sync(ctx, reason)
		}
	}
}

// waitForBaseline gives docker compose time to (re)create the baseline
// containers when the whole stack starts together with the controller.
func (p *Production) waitForBaseline(ctx context.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready := true
		for _, svc := range p.cfg.Services {
			if _, err := p.rt.BaselineOf(ctx, svc.Name); err != nil {
				ready = false
				break
			}
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// Trigger requests an immediate synchronisation (e.g. "manual").
func (p *Production) Trigger(reason string) {
	select {
	case p.trigger <- reason:
	default: // one is already queued
	}
}

// State returns a snapshot for the API.
func (p *Production) State() api.Production {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state
	s.Services = append([]api.ProductionService(nil), p.state.Services...)
	s.History = append([]api.ProductionUpdate(nil), p.state.History...)
	return s
}

// BuildLog streams the log of the most recent production build of service.
func (p *Production) BuildLog(service string, w io.Writer) error {
	f, err := os.Open(filepath.Join(p.dataDir, "production", service+".log"))
	if err != nil {
		return fmt.Errorf("no production build log for %q", service)
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func (p *Production) sync(ctx context.Context, trigger string) {
	p.syncMu.Lock()
	defer p.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	head, base, err := p.resolve(ctx)
	if err != nil {
		p.finishCheck(api.ProductionError, err.Error())
		return
	}

	type change struct {
		svc      project.Service
		current  runtime.Baseline
		desired  string
		failKey  string
		fromRepo bool
	}
	var changes []change
	services := make([]api.ProductionService, 0, len(p.cfg.Services))
	var problems []string
	for _, svc := range p.cfg.Services {
		bl, err := p.rt.BaselineOf(ctx, svc.Name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		headTree, err := p.repo.Tree(ctx, head, svc.Build.Context)
		if err != nil {
			problems = append(problems, svc.Name+": "+err.Error())
			continue
		}
		baseTree, _ := p.repo.Tree(ctx, base, svc.Build.Context)
		desired, source := bl.UpstreamImage, "upstream"
		if headTree != baseTree {
			desired, source = fmt.Sprintf("branchbox/%s-%s:prod-%s", p.cfg.Project, svc.Name, headTree[:12]), "branch"
		}
		services = append(services, api.ProductionService{Name: svc.Name, Image: desired, Source: source})
		if bl.Image == desired {
			continue
		}
		key := svc.Name + "@" + headTree
		p.mu.Lock()
		skip := p.failed[key] && trigger != "manual"
		p.mu.Unlock()
		if skip {
			problems = append(problems, svc.Name+": an earlier attempt to deploy this version failed or was rolled back (run 'branchbox production update' to retry)")
			continue
		}
		changes = append(changes, change{svc: svc, current: bl, desired: desired, failKey: key, fromRepo: source == "branch"})
	}

	subject := p.repo.Subject(ctx, head)
	if len(changes) == 0 {
		observed := p.observe(ctx)
		p.mu.Lock()
		// Commit is the last commit production fully runs.
		if len(problems) == 0 {
			p.state.Commit, p.state.Subject = head, subject
		}
		p.state.Services = observed
		p.mu.Unlock()
		if len(problems) > 0 {
			p.finishCheck(api.ProductionError, strings.Join(problems, "; "))
		} else {
			p.finishCheck(api.ProductionInSync, "")
		}
		return
	}

	p.mu.Lock()
	from := p.state.Commit
	p.state.Status = api.ProductionUpdating
	p.mu.Unlock()
	start := time.Now()
	var updated []string
	for _, c := range changes {
		log.Printf("production: updating %s to %s (%s)", c.svc.Name, c.desired, short(head))
		if err := p.apply(ctx, head, c.svc, c.current, c.desired, c.fromRepo); err != nil {
			log.Printf("production: %s: %v", c.svc.Name, err)
			problems = append(problems, c.svc.Name+": "+brief(err.Error()))
			p.mu.Lock()
			p.failed[c.failKey] = true
			p.mu.Unlock()
			continue
		}
		updated = append(updated, c.svc.Name)
	}

	// Smoke test the application through its entry service. A service can
	// start and listen yet still break the application, so if the shop no
	// longer answers, every service changed by this update is rolled back.
	if len(updated) > 0 {
		if err := p.smokeTest(ctx); err != nil {
			log.Printf("production: smoke test failed after updating %v: %v; rolling back", updated, err)
			msg := fmt.Sprintf("smoke test failed (%v); rolled back %s", err, strings.Join(updated, ", "))
			for _, c := range changes {
				if !slices.Contains(updated, c.svc.Name) {
					continue
				}
				labels := map[string]string{"branchbox.production-commit": from}
				if rbErr := p.rt.ReplaceBaseline(context.Background(), c.svc.Name, c.svc.Port, c.current.Image, labels); rbErr != nil {
					msg += fmt.Sprintf("; ROLLBACK OF %s FAILED: %v", c.svc.Name, rbErr)
				}
				p.mu.Lock()
				p.failed[c.failKey] = true
				p.mu.Unlock()
			}
			problems = append(problems, msg)
			updated = nil
		}
	}
	services = p.observe(ctx)

	entry := api.ProductionUpdate{
		Time: start.UTC(), Trigger: trigger, From: from, To: head, Subject: subject,
		Services: updated, Seconds: time.Since(start).Seconds(), Error: strings.Join(problems, "; "),
	}
	p.mu.Lock()
	if len(problems) == 0 {
		p.state.Commit, p.state.Subject = head, subject
	}
	p.state.Services = services
	p.state.History = append(p.state.History, entry)
	if n := len(p.state.History); n > 20 {
		p.state.History = p.state.History[n-20:]
	}
	p.mu.Unlock()
	if len(problems) > 0 {
		p.finishCheck(api.ProductionError, entry.Error)
	} else {
		p.finishCheck(api.ProductionInSync, "")
	}
	if len(problems) > 0 {
		log.Printf("production: update to %s %q failed: %s", short(head), subject, entry.Error)
	} else {
		log.Printf("production: now at %s %q (%d services updated in %.1fs)", short(head), subject, len(updated), entry.Seconds)
	}
}

// apply builds (if needed) and swaps one service, rolling back on failure.
func (p *Production) apply(ctx context.Context, head string, svc project.Service, current runtime.Baseline, image string, fromRepo bool) error {
	if fromRepo && !p.rt.ImageExists(ctx, image) {
		if err := p.build(ctx, head, svc, image); err != nil {
			return err
		}
	}
	labels := map[string]string{
		runtime.LabelUpstreamImage:    current.UpstreamImage,
		"branchbox.production-commit": head,
	}
	// The runtime restores the previous version itself if this one fails.
	return p.rt.ReplaceBaseline(ctx, svc.Name, svc.Port, image, labels)
}

// smokeTest requests the entry service's smoke path (default "/") and
// accepts any non-5xx answer, retrying briefly while services warm up.
func (p *Production) smokeTest(ctx context.Context) error {
	entry, ok := p.cfg.Service(p.cfg.Entry.Service)
	if !ok || entry.Protocol != "http" {
		return nil // nothing generic to check
	}
	url := fmt.Sprintf("http://%s:%d%s", entry.Name, entry.Port, p.cfg.Production.SmokePath)
	client := &http.Client{Timeout: 10 * time.Second}
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		resp, err := client.Get(url)
		if err != nil {
			last = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return nil
		}
		last = fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return last
}

// observe reports what each production service is actually running.
func (p *Production) observe(ctx context.Context) []api.ProductionService {
	out := make([]api.ProductionService, 0, len(p.cfg.Services))
	for _, svc := range p.cfg.Services {
		bl, err := p.rt.BaselineOf(ctx, svc.Name)
		if err != nil {
			out = append(out, api.ProductionService{Name: svc.Name, Image: "not running", Source: "-"})
			continue
		}
		source := "branch"
		if bl.Image == bl.UpstreamImage {
			source = "upstream"
		}
		out = append(out, api.ProductionService{Name: svc.Name, Image: bl.Image, Source: source})
	}
	return out
}

func (p *Production) build(ctx context.Context, head string, svc project.Service, image string) error {
	dir := filepath.Join(p.dataDir, "production")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	logFile, err := os.Create(filepath.Join(dir, svc.Name+".log"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	src := filepath.Join(dir, "src-"+svc.Name)
	_ = os.RemoveAll(src)
	defer os.RemoveAll(src)
	fmt.Fprintf(logFile, "[branchbox] building %s for production from %s@%s\n", svc.Name, p.branch, short(head))
	if err := p.repo.Export(ctx, head, svc.Build.Context, src); err != nil {
		return err
	}
	return p.rt.Build(ctx, runtime.BuildSpec{
		ContextDir: src,
		Dockerfile: svc.Build.Dockerfile,
		Tag:        image,
		Labels:     map[string]string{"branchbox.production": "true", "branchbox.commit": head},
	}, logFile)
}

func (p *Production) resolve(ctx context.Context) (head, base string, err error) {
	if err := p.repo.Fetch(ctx); err != nil {
		return "", "", err
	}
	if head, err = p.repo.Resolve(ctx, p.branch); err != nil {
		return "", "", err
	}
	baseRef := p.cfg.Repository.BaseRef
	if baseRef == "" {
		return "", "", errors.New("repository.baseRef must name the release the baseline images were built from")
	}
	if base, err = p.repo.Resolve(ctx, baseRef); err != nil {
		return "", "", fmt.Errorf("baseRef: %w", err)
	}
	return head, base, nil
}

func (p *Production) finishCheck(status, lastErr string) {
	now := time.Now().UTC()
	p.mu.Lock()
	p.state.Status, p.state.LastError, p.state.LastChecked = status, lastErr, &now
	snapshot := p.state
	p.mu.Unlock()
	if raw, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
		_ = os.WriteFile(p.statePath(), raw, 0o644)
	}
}

func (p *Production) statePath() string { return filepath.Join(p.dataDir, "production.json") }

// brief keeps error messages readable in the update history: the last
// non-empty line of a multi-line tool error, capped in length.
func brief(msg string) string {
	full := msg
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "Run 'docker") {
			msg = l
			break
		}
	}
	if len(msg) > 220 {
		msg = msg[:220] + "..."
	}
	if strings.Contains(full, "(rolled back to") && !strings.Contains(msg, "(rolled back to") {
		msg += " (rolled back to the previous version)"
	}
	return msg
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
