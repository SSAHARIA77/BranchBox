// Package api defines the wire types shared by the BranchBox controller,
// the proxies (sidecar and edge) and the CLI.
package api

import "time"

// Info tells clients how this BranchBox installation is reached.
type Info struct {
	Project      string `json:"project"`
	EntryService string `json:"entryService"`
	EdgeDomain   string `json:"edgeDomain"` // sandbox URLs are http://<id>.<EdgeDomain>:<EdgePort>
	EdgePort     string `json:"edgePort"`
	Repository   string `json:"repository"`
}

// Service is one routable microservice of the onboarded project.
type Service struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// Route diverts requests for Service that carry environment ID Env to the
// sandbox listening at Address (host:port). Any (service, env) pair without
// a route is served by the baseline.
type Route struct {
	Service string `json:"service"`
	Env     string `json:"env"`
	Address string `json:"address"`
}

// RouteTable is the versioned routing state that proxies long-poll for.
type RouteTable struct {
	Version  uint64    `json:"version"`
	Services []Service `json:"services"`
	Routes   []Route   `json:"routes"`
}

// SandboxStatus is the lifecycle phase of a sandbox.
type SandboxStatus string

const (
	StatusPending  SandboxStatus = "pending"
	StatusBuilding SandboxStatus = "building"
	StatusStarting SandboxStatus = "starting"
	StatusReady    SandboxStatus = "ready"
	StatusFailed   SandboxStatus = "failed"
	StatusDeleting SandboxStatus = "deleting"
)

// Sandbox is one isolated, ephemeral deployment of a single service built
// from a development branch. Its ID doubles as the environment ID.
type Sandbox struct {
	ID        string        `json:"id"`
	Service   string        `json:"service"`
	Branch    string        `json:"branch"`
	Commit    string        `json:"commit,omitempty"`
	Image     string        `json:"image,omitempty"`
	Address   string        `json:"address,omitempty"`
	Status    SandboxStatus `json:"status"`
	Error     string        `json:"error,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	ReadyAt   *time.Time    `json:"readyAt,omitempty"`
}

// SetupSeconds is the time from creation request to the sandbox accepting
// tagged traffic (one of the evaluation metrics).
func (s Sandbox) SetupSeconds() float64 {
	if s.ReadyAt == nil {
		return 0
	}
	return s.ReadyAt.Sub(s.CreatedAt).Seconds()
}

// CreateSandboxRequest asks the controller to build Service from Branch.
// ID is optional; the controller generates one when it is empty.
type CreateSandboxRequest struct {
	ID      string `json:"id,omitempty"`
	Service string `json:"service"`
	Branch  string `json:"branch"`
}

// Decision values recorded in routing events.
const (
	DecisionBaseline = "baseline"
	DecisionSandbox  = "sandbox"
)

// Event records one routing decision made by a proxy for one request hop.
// Events sharing a TraceID together describe the full path of a request.
type Event struct {
	Time          time.Time `json:"time"`
	TraceID       string    `json:"traceId,omitempty"`
	Env           string    `json:"env,omitempty"`
	Source        string    `json:"source"`
	SourceSandbox string    `json:"sourceSandbox,omitempty"`
	Dest          string    `json:"dest"`
	Decision      string    `json:"decision"`
	Target        string    `json:"target"`
	Protocol      string    `json:"protocol"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	Status        int       `json:"status"`
	GRPCStatus    string    `json:"grpcStatus,omitempty"`
	DurationMs    float64   `json:"durationMs"`
}
