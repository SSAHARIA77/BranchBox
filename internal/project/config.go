// Package project loads branchbox.yaml, the only application-specific
// input BranchBox needs. Onboarding a different application means writing
// a new branchbox.yaml (generated automatically by the discovery module in
// Major Project-II); no BranchBox code changes.
package project

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"branchbox/internal/api"
)

// Config describes one onboarded project.
type Config struct {
	Project    string     `yaml:"project" json:"project"`
	Repository Repository `yaml:"repository" json:"repository"`
	Runtime    Runtime    `yaml:"runtime" json:"runtime"`
	Entry      Entry      `yaml:"entry" json:"entry"`
	Limits     Limits     `yaml:"limits" json:"limits"`
	Production Production `yaml:"production" json:"production"`
	Services   []Service  `yaml:"services" json:"services"`
}

// Repository is the Git repository sandboxes are built from.
type Repository struct {
	URL     string `yaml:"url" json:"url"`
	BaseRef string `yaml:"baseRef" json:"baseRef"` // the ref the baseline was deployed from
}

// Runtime locates the running baseline.
type Runtime struct {
	ComposeProject string `yaml:"composeProject" json:"composeProject"`
	Network        string `yaml:"network" json:"network"`
}

// Entry names the service that external traffic enters through.
type Entry struct {
	Service string `yaml:"service" json:"service"`
}

// Limits bound resource usage.
type Limits struct {
	MaxSandboxes int `yaml:"maxSandboxes" json:"maxSandboxes"`
}

// Production makes the baseline follow a branch: when the branch moves
// (a pull request is merged), the services whose code changed are rebuilt
// and swapped into production.
type Production struct {
	Branch      string `yaml:"branch" json:"branch"`           // default "main"
	AutoUpdate  *bool  `yaml:"autoUpdate" json:"autoUpdate"`   // default true
	PollSeconds int    `yaml:"pollSeconds" json:"pollSeconds"` // default 30
	// SmokePath is requested on the entry service after every production
	// update; a 5xx answer or no answer rolls the update back. Default "/".
	SmokePath string `yaml:"smokePath" json:"smokePath"`
}

// Service is one microservice that can be sandboxed.
type Service struct {
	Name     string `yaml:"name" json:"name"`
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol"`
	Build    Build  `yaml:"build" json:"build"`
}

// Build tells BranchBox how to build the service from source.
type Build struct {
	Context    string `yaml:"context" json:"context"`       // directory within the repository
	Dockerfile string `yaml:"dockerfile" json:"dockerfile"` // relative to Context
}

// Load reads and validates a branchbox.yaml file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Limits.MaxSandboxes == 0 {
		c.Limits.MaxSandboxes = 10
	}
	if c.Production.Branch == "" {
		c.Production.Branch = "main"
	}
	if c.Production.AutoUpdate == nil {
		auto := true
		c.Production.AutoUpdate = &auto
	}
	if c.Production.PollSeconds <= 0 {
		c.Production.PollSeconds = 30
	}
	if c.Production.SmokePath == "" {
		c.Production.SmokePath = "/"
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) validate() error {
	switch {
	case c.Project == "":
		return fmt.Errorf("project is required")
	case c.Repository.URL == "":
		return fmt.Errorf("repository.url is required")
	case c.Runtime.ComposeProject == "" || c.Runtime.Network == "":
		return fmt.Errorf("runtime.composeProject and runtime.network are required")
	case len(c.Services) == 0:
		return fmt.Errorf("at least one service is required")
	}
	seen := map[string]bool{}
	for i := range c.Services {
		s := &c.Services[i]
		if s.Name == "" || s.Port <= 0 {
			return fmt.Errorf("services[%d]: name and port are required", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("service %q is listed twice", s.Name)
		}
		seen[s.Name] = true
		if s.Build.Context == "" {
			return fmt.Errorf("service %q: build.context is required", s.Name)
		}
		if s.Build.Dockerfile == "" {
			s.Build.Dockerfile = "Dockerfile"
		}
		if s.Protocol == "" {
			s.Protocol = "http"
		}
	}
	if c.Entry.Service != "" && !seen[c.Entry.Service] {
		return fmt.Errorf("entry.service %q is not a listed service", c.Entry.Service)
	}
	return nil
}

// Service returns the named service.
func (c *Config) Service(name string) (Service, bool) {
	for _, s := range c.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// APIServices returns the routable services in wire format.
func (c *Config) APIServices() []api.Service {
	out := make([]api.Service, 0, len(c.Services))
	for _, s := range c.Services {
		out = append(out, api.Service{Name: s.Name, Port: s.Port, Protocol: s.Protocol})
	}
	return out
}
