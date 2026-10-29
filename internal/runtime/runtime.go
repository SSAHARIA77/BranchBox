// Package runtime abstracts where sandboxes run. Major Project-I ships a
// Docker implementation; a Kubernetes implementation can be added later
// behind the same interface without touching the controller.
package runtime

import (
	"context"
	"io"
)

// BuildSpec describes an image build from an exported source directory.
type BuildSpec struct {
	ContextDir string
	Dockerfile string // relative to ContextDir
	Tag        string
	Labels     map[string]string
}

// SandboxSpec describes one sandboxed service.
type SandboxSpec struct {
	ID      string // sandbox / environment ID
	Service string
	Port    int
	Image   string
}

// Component selects which part of a sandbox to read logs from.
type Component string

const (
	ComponentApp     Component = "app"
	ComponentSidecar Component = "sidecar"
)

// Runtime builds images and runs sandboxes next to the baseline.
type Runtime interface {
	// Build builds spec, streaming build output to log.
	Build(ctx context.Context, spec BuildSpec, log io.Writer) error
	// Deploy starts the sandbox (with its sidecar) and returns the address
	// other services must dial to reach it, once it accepts connections.
	Deploy(ctx context.Context, spec SandboxSpec) (address string, err error)
	// Destroy removes everything Deploy created. It is idempotent.
	Destroy(ctx context.Context, spec SandboxSpec) error
	// RemoveImage deletes a sandbox image built by Build (best effort).
	RemoveImage(ctx context.Context, image string)
	// Alive reports whether a previously deployed sandbox is still running.
	Alive(ctx context.Context, spec SandboxSpec) bool
	// Logs writes a component's logs to w.
	Logs(ctx context.Context, spec SandboxSpec, c Component, tail int, follow bool, w io.Writer) error
}
