package ports

import (
	"testing"
)

func TestPortValidation(t *testing.T) {
	// defaults filled
	p := Port{Name: "web", Target: 3000}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Listen != DefaultListen || p.Protocol != "tcp" || p.Published != "3000" {
		t.Errorf("defaults wrong: %+v", p)
	}
}

func TestPortAuto(t *testing.T) {
	p := Port{Name: "debug", Target: 9229, Published: AutoPublished}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPortValidationErrors(t *testing.T) {
	cases := []Port{
		{Name: "", Target: 3000},
		{Name: "x", Target: 0},
		{Name: "x", Target: 3000, Published: "abc"},
		{Name: "x", Target: 3000, Published: "0"},
		{Name: "x", Target: 3000, Published: "70000"},
		{Name: "x", Target: 3000, Published: "80"},
		{Name: "x", Target: 3000, Protocol: "udp"},
		{Name: "x", Target: 3000, Listen: "not-an-ip"},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d should fail: %+v", i, c)
		}
	}
}

func TestAllocateReusesAssignment(t *testing.T) {
	a := NewAllocator(DefaultListen)
	inUse := func(p uint16) bool { return p == 4000 }
	p1, err := a.Allocate("web", 3900, 4100, inUse)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == 4000 {
		t.Errorf("allocator picked in-use port")
	}
	p2, err := a.Allocate("web", 3900, 4100, inUse)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Errorf("reuse failed: %d vs %d", p1, p2)
	}
}

func TestAllocateConflictThenNewPort(t *testing.T) {
	a := NewAllocator(DefaultListen)
	// First allocation picks 3900.
	p1, err := a.Allocate("web", 3900, 4100, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Later, that port becomes busy; a new allocation must pick a new port
	// (design §11.4: conflict -> new port, report change).
	p2, err := a.Allocate("web", 3900, 4100, func(p uint16) bool { return p == p1 })
	if err != nil {
		t.Fatal(err)
	}
	if p2 == p1 {
		t.Errorf("expected different port on conflict, got %d", p2)
	}
}

func TestAllocateExhausted(t *testing.T) {
	a := NewAllocator(DefaultListen)
	_, err := a.Allocate("web", 3900, 3900, func(p uint16) bool { return p == 3900 })
	if err == nil {
		t.Fatal("expected exhaustion error")
	}
}

func TestAllocateSkipsPrivileged(t *testing.T) {
	a := NewAllocator(DefaultListen)
	p, err := a.Allocate("web", 80, 1100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p < 1024 {
		t.Errorf("allocator returned privileged port %d", p)
	}
}

func TestEndpointAddr(t *testing.T) {
	e := Endpoint{Name: "web", Target: 3000, Published: 3000, Listen: "127.0.0.1", Protocol: "tcp"}
	if e.Addr() != "127.0.0.1:3000" {
		t.Errorf("Addr = %q", e.Addr())
	}
}
