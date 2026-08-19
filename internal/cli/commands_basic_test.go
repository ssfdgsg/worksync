package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestLangImageMapping(t *testing.T) {
	cases := map[string]string{
		"go":     "golang:1.24",
		"python": "python:3.13",
		"rust":   "rust:1.83",
		"node":   "node:24",
		"":       "debian:bookworm-slim",
		"bogus":  "debian:bookworm-slim",
	}
	for in, want := range cases {
		if got := langImage(in); got != want {
			t.Errorf("langImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCustomPortYAML(t *testing.T) {
	got, err := customPortYAML("api:8080")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: api", "target: 8080", "published: auto"} {
		if !strings.Contains(got, want) {
			t.Errorf("customPortYAML(api:8080) missing %q:\n%s", want, got)
		}
	}
	// bare number defaults to name web
	got, err = customPortYAML("9090")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "name: web") || !strings.Contains(got, "target: 9090") {
		t.Errorf("bare number: %s", got)
	}
	// invalid target rejected
	if _, err := customPortYAML("api:notaport"); err == nil {
		t.Error("expected error for invalid target")
	}
}

func TestBuildInitYAML(t *testing.T) {
	got := buildInitYAML("demo", "golang:1.24", "")
	for _, want := range []string{"name: demo", "image: golang:1.24", "policy: tracked"} {
		if !strings.Contains(got, want) {
			t.Errorf("buildInitYAML missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ports:") {
		t.Errorf("no ports segment expected, got:\n%s", got)
	}
	// with a port block it renders
	withPort := buildInitYAML("demo", "node:24", defaultPortYAML())
	if !strings.Contains(withPort, "ports:") || !strings.Contains(withPort, "target: 3000") {
		t.Errorf("with ports expected:\n%s", withPort)
	}
}

func TestInitWizardFullFlow(t *testing.T) {
	// answers: name=myapp, image=3 (python), port=custom api:8080, confirm=yes
	app := &App{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("myapp\n3\napi:8080\ny\n"), Interactive: true}
	name, image, content, err := app.initWizard("demo")
	if err != nil {
		t.Fatal(err)
	}
	if name != "myapp" {
		t.Errorf("name = %q, want myapp", name)
	}
	if image != "python:3.13" {
		t.Errorf("image = %q, want python:3.13", image)
	}
	if !strings.Contains(content, "name: myapp") || !strings.Contains(content, "target: 8080") {
		t.Errorf("content mismatch:\n%s", content)
	}
}

func TestInitWizardDefaultsOnEnter(t *testing.T) {
	// all empty answers -> defaults: default name, generic image, default port
	app := &App{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("\n\n\n\n"), Interactive: true}
	name, image, content, err := app.initWizard("demo")
	if err != nil {
		t.Fatal(err)
	}
	if name != "demo" {
		t.Errorf("default name = %q, want demo", name)
	}
	if image != "debian:bookworm-slim" {
		t.Errorf("default image = %q, want debian:bookworm-slim", image)
	}
	if !strings.Contains(content, "target: 3000") {
		t.Errorf("default port missing:\n%s", content)
	}
}

func TestInitWizardCancelIsClean(t *testing.T) {
	// answers: name=skip, image=skip, port=skip, confirm=n (cancel)
	app := &App{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("\n\n\nn\n"), Interactive: true}
	_, _, _, err := app.initWizard("demo")
	if !errors.Is(err, errInitCancelled) {
		t.Errorf("want errInitCancelled, got %v", err)
	}
}

func TestInitWizardWorksWithoutTTY(t *testing.T) {
	// Interactive=false (piped stdin, e.g. wrapped/GUI terminal): the wizard
	// must still run and allow custom input.
	app := &App{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("svc\nubuntu:22.04\n9000\ny\n")}
	name, image, content, err := app.initWizard("demo")
	if err != nil {
		t.Fatal(err)
	}
	if name != "svc" || image != "ubuntu:22.04" {
		t.Errorf("got name=%q image=%q, want svc/ubuntu:22.04", name, image)
	}
	if !strings.Contains(content, "target: 9000") {
		t.Errorf("custom port missing:\n%s", content)
	}
}

func TestInitWizardCustomImageReference(t *testing.T) {
	app := &App{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("\nghcr.io/org/img:v2\n0\ny\n"), Interactive: true}
	_, image, _, err := app.initWizard("demo")
	if err != nil {
		t.Fatal(err)
	}
	if image != "ghcr.io/org/img:v2" {
		t.Errorf("custom image = %q, want ghcr.io/org/img:v2", image)
	}
}

func TestInitWizardCustomImageURL(t *testing.T) {
	// registry path with port
	app := &App{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("\nregistry.example.com:5000/org/img:v1\n\ny\n")}
	_, image, _, err := app.initWizard("demo")
	if err != nil {
		t.Fatal(err)
	}
	if image != "registry.example.com:5000/org/img:v1" {
		t.Errorf("port URL = %q", image)
	}
	// full https URL passes through
	app2 := &App{Stdout: &bytes.Buffer{}, Stdin: strings.NewReader("\nhttps://registry.example.com/org/img:v2\n\ny\n")}
	_, image, _, err = app2.initWizard("demo")
	if err != nil {
		t.Fatal(err)
	}
	if image != "https://registry.example.com/org/img:v2" {
		t.Errorf("https URL = %q", image)
	}
}

func TestColorDisabledNonInteractive(t *testing.T) {
	app := &App{Stdout: &bytes.Buffer{}}
	if got := app.green("x"); got != "x" {
		t.Errorf("non-interactive color = %q, want plain x", got)
	}
	app2 := &App{Stdout: &bytes.Buffer{}, Interactive: true}
	if got := app2.green("x"); got == "x" {
		t.Errorf("interactive color should wrap, got %q", got)
	}
}
