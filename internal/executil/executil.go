// Package executil runs external commands (podman, restic, ssh/sftp) with
// structured capture, redaction and context support (design §23: subprocess
// output must be structured and secrets masked).
package executil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Result captures a finished command's output.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Combined returns stdout and stderr concatenated.
func (r Result) Combined() string {
	if r.Stderr == "" {
		return r.Stdout
	}
	return strings.TrimRight(r.Stdout, "\n") + "\n" + r.Stderr
}

// Error is a command failure with the exit code and stderr attached.
type Error struct {
	Name     string
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s %s: exit status %d: %s", e.Name, strings.Join(e.Args, " "), e.ExitCode, strings.TrimSpace(e.Stderr))
}

// Options configures a Run call.
type Options struct {
	Env    []string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer // when set, output is streamed instead of captured
	Stderr io.Writer
	Redact []string // secrets to mask in returned strings
}

// Option mutates Options.
type Option func(*Options)

func WithEnv(env []string) Option         { return func(o *Options) { o.Env = env } }
func WithDir(dir string) Option           { return func(o *Options) { o.Dir = dir } }
func WithStdin(r io.Reader) Option        { return func(o *Options) { o.Stdin = r } }
func WithStdout(w io.Writer) Option       { return func(o *Options) { o.Stdout = w } }
func WithStderr(w io.Writer) Option       { return func(o *Options) { o.Stderr = w } }
func WithRedact(secrets ...string) Option { return func(o *Options) { o.Redact = secrets } }

// Redact masks every occurrence of the given secrets in s.
func Redact(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// Run executes name with args. When Options.Stdout/Stderr are nil the output
// is captured (bounded) and returned in Result; secrets listed in Redact are
// masked in the captured output. A non-zero exit returns *Error.
func Run(ctx context.Context, name string, args []string, opts ...Option) (Result, error) {
	o := &Options{}
	for _, f := range opts {
		f(o)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), o.Env...)
	if o.Dir != "" {
		cmd.Dir = o.Dir
	}
	if o.Stdin != nil {
		cmd.Stdin = o.Stdin
	} else {
		// inherit the CLI's stdin so interactive commands (worksync exec,
		// shell) can read from the user.
		cmd.Stdin = os.Stdin
	}
	var outBuf, errBuf bytes.Buffer
	if o.Stdout != nil {
		cmd.Stdout = o.Stdout
	} else {
		cmd.Stdout = &outBuf
	}
	if o.Stderr != nil {
		cmd.Stderr = o.Stderr
	} else {
		cmd.Stderr = &errBuf
	}
	err := cmd.Run()
	res := Result{
		Stdout:   Redact(outBuf.String(), o.Redact),
		Stderr:   Redact(errBuf.String(), o.Redact),
		ExitCode: exitCode(err, cmd),
	}
	if err != nil {
		return res, &Error{Name: name, Args: args, ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return res, nil
}

func exitCode(err error, cmd *exec.Cmd) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}

// LookPath returns the absolute path of a binary or a descriptive error.
func LookPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH (install it or add it to PATH)", name)
	}
	return p, nil
}
