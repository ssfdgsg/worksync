// Package ports defines the port declaration schema and the PortPublisher
// interface (design §11).
package ports

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
)

// Port is a user-declared container port mapping (design §11.1).
type Port struct {
	Name      string `yaml:"name"`
	Target    uint16 `yaml:"target"`
	Published string `yaml:"published"` // "<port>" or "auto" (design §11.1)
	Listen    string `yaml:"listen"`    // host listen address, default 127.0.0.1
	Protocol  string `yaml:"protocol"`  // v0: only "tcp"
}

// AutoPublished marks ports whose host port should be allocated.
const AutoPublished = "auto"

// DefaultListen is the default host listen address (design §11.5).
const DefaultListen = "127.0.0.1"

// Validate checks a single port declaration.
func (p *Port) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if p.Target == 0 {
		return fmt.Errorf("target port is required")
	}
	if p.Listen == "" {
		p.Listen = DefaultListen
	}
	if p.Protocol == "" {
		p.Protocol = "tcp"
	}
	if p.Protocol != "tcp" {
		return fmt.Errorf("protocol %q not supported in v0 (only tcp)", p.Protocol)
	}
	if p.Published == "" {
		p.Published = strconv.Itoa(int(p.Target))
	}
	if p.Published != AutoPublished {
		n, err := strconv.Atoi(p.Published)
		if err != nil {
			return fmt.Errorf("published %q must be a port number or %q", p.Published, AutoPublished)
		}
		if n < 1 || n > 65535 {
			return fmt.Errorf("published port %d out of range", n)
		}
		if n < 1024 {
			// Design §11.5: rootless mode cannot reliably bind <1024.
			return fmt.Errorf("published port %d is below 1024; rootless mode cannot reliably bind privileged ports, choose a high port", n)
		}
	}
	if ip := net.ParseIP(p.Listen); ip == nil && p.Listen != "" {
		return fmt.Errorf("listen %q is not a valid IP address", p.Listen)
	}
	return nil
}

// Endpoint is the concrete, allocated result of publishing a port
// (design §11.4).
type Endpoint struct {
	ProjectID string `json:"project"`
	Name      string `json:"name"`
	Target    uint16 `json:"target"`
	Published uint16 `json:"published"`
	Listen    string `json:"listen"`
	Protocol  string `json:"protocol"`
}

// Addr returns "listen:published", the address users connect to.
func (e Endpoint) Addr() string { return net.JoinHostPort(e.Listen, strconv.Itoa(int(e.Published))) }

// PortPublisher is the interface implemented by backends that expose
// container ports on the host (design §11.4).
type PortPublisher interface {
	Publish(ctx context.Context, projectID string, spec Port) (Endpoint, error)
	Unpublish(ctx context.Context, projectID string, name string) error
	List(ctx context.Context, projectID string) ([]Endpoint, error)
}

// Allocator picks free host ports, recording assignments so that stop/start
// cycles reuse the same host port (design §11.4).
type Allocator struct {
	// listen is the host address the port would bind to.
	listen string
	// assigned maps port name -> allocated host port.
	assigned map[string]uint16
}

// NewAllocator creates an allocator for the given listen address.
func NewAllocator(listen string) *Allocator {
	return &Allocator{listen: listen, assigned: map[string]uint16{}}
}

// Reuse reports the previously allocated host port for name, if any.
func (a *Allocator) Reuse(name string) (uint16, bool) {
	p, ok := a.assigned[name]
	return p, ok
}

// Allocate finds a free host port in [lo, hi] for name, skipping inUse. It
// records the assignment so a later call returns the same port. When
// conflict arises across restarts the new port differs clearly.
func (a *Allocator) Allocate(name string, lo, hi uint16, inUse func(uint16) bool) (uint16, error) {
	if p, ok := a.assigned[name]; ok {
		if inUse == nil || !inUse(p) {
			return p, nil
		}
	}
	for p := lo; p <= hi; p++ {
		if p < 1024 {
			continue
		}
		if inUse != nil && inUse(p) {
			continue
		}
		a.assigned[name] = p
		return p, nil
	}
	return 0, fmt.Errorf("no free host port in [%d, %d]", lo, hi)
}

// EndpointsSorted sorts endpoints by name for deterministic output.
func EndpointsSorted(es []Endpoint) {
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
}
