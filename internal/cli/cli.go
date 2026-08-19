// Package cli implements the worksync command-line interface (design §18).
// It wires Project Spec resolution, the state DB, locks, backends and the
// runtime/transport layers behind a small subcommand dispatcher.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"worksync/internal/backend"
	"worksync/internal/project"
	"worksync/internal/state"
	"worksync/internal/store"
)

// Build metadata injected at link time (-ldflags). A plain go build leaves
// them at their defaults so the binary still identifies itself.
var (
	Version   = "dev"
	BuildTime = ""
)

// ErrNotImplemented reports a command that is registered in v0's CLI surface
// but not yet implemented in this build milestone.
var ErrNotImplemented = errors.New("not implemented in this build")

// WbError is an error with a stable error code (design §18.5), e.g.
// WB_PORT_IN_USE.
type WbError struct {
	Code    string
	Message string
}

func (e *WbError) Error() string { return e.Message }

// Stable error codes.
const (
	CodePortInUse   = "WB_PORT_IN_USE"
	CodeLocked      = "WB_LOCKED"
	CodeNotFound    = "WB_NOT_FOUND"
	CodeUnsupported = "WB_UNSUPPORTED"
	CodeMissingTool = "WB_MISSING_TOOL"
	CodeConflict    = "WB_CONFLICT"
	CodeConfig      = "WB_CONFIG"
	CodeInternal    = "WB_INTERNAL"
)

// App carries the shared environment for command execution.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader // interactive input (menu prompts); nil means non-interactive
	Layout *store.Layout
	DB     *state.DB
	Debug  bool
	JSON   bool
	// Interactive reports whether both stdin and stdout are real terminals:
	// guided prompts and colored output are only enabled then.
	Interactive bool
	wd          string
}

// Command is one subcommand.
type Command struct {
	Name  string
	Usage string
	Short string
	Run   func(ctx context.Context, app *App, args []string) error
}

// Env is the shared runtime environment resolved once per invocation.
type Env struct {
	App     *App
	Backend backend.Backend
	Workdir string
}

// NewApp builds an App with a resolved store layout and state DB.
func NewApp(stdout, stderr io.Writer) (*App, error) {
	layout, err := store.DefaultLayout()
	if err != nil {
		return nil, err
	}
	if err := layout.Ensure(); err != nil {
		return nil, err
	}
	db, err := state.Open(layout.StateDB)
	if err != nil {
		return nil, err
	}
	in, _ := os.Stdin.Stat()
	out, _ := stdout.(*os.File)
	interactive := false
	if in != nil && out != nil {
		fo, err := out.Stat()
		interactive = err == nil &&
			(in.Mode()&os.ModeCharDevice) != 0 && (fo.Mode()&os.ModeCharDevice) != 0
	}
	return &App{Stdout: stdout, Stderr: stderr, Stdin: os.Stdin, Layout: layout, DB: db, Interactive: interactive}, nil
}

// Close releases the state DB.
func (a *App) Close() error { return a.DB.Close() }

// Commands returns all registered subcommands in declaration order.
func Commands() []Command { return commands }

var commands []Command

func init() {
	commands = []Command{
		{Name: "init", Usage: "init [--name NAME] [--image IMAGE] [--lang LANG] [--force]", Short: "scaffold a new worksync.yaml", Run: cmdInit},
		{Name: "up", Usage: "up [--backend auto]", Short: "create or start the development container", Run: cmdUp},
		{Name: "status", Usage: "status [--json]", Short: "show project and container state", Run: cmdStatus},
		{Name: "shell", Usage: "shell [-- command...]", Short: "open an interactive shell in the container", Run: cmdShell},
		{Name: "exec", Usage: "exec -- command...", Short: "run a command in the container", Run: cmdExec},
		{Name: "stop", Usage: "stop", Short: "stop the container (keeps rootfs/volumes)", Run: cmdStop},
		{Name: "start", Usage: "start", Short: "start the container", Run: cmdStart},
		{Name: "rm", Usage: "rm [--volumes --yes]", Short: "remove the container (managed volumes need confirmation)", Run: cmdRm},
		{Name: "ports", Usage: "ports [--json]", Short: "list published ports", Run: cmdPorts},
		{Name: "expose", Usage: "expose TARGET[:HOST]", Short: "publish a container port", Run: cmdExpose},
		{Name: "unexpose", Usage: "unexpose PORT", Short: "stop publishing a port", Run: cmdUnexpose},
		{Name: "diff", Usage: "diff", Short: "show differences from the last commit", Run: cmdDiff},
		{Name: "commit", Usage: "commit -m MESSAGE", Short: "commit environment and selected volumes", Run: cmdCommit},
		{Name: "log", Usage: "log [--json]", Short: "show commit history", Run: cmdLog},
		{Name: "tag", Usage: "tag NAME [COMMIT-OR-REF]", Short: "tag a commit", Run: cmdTag},
		{Name: "rollback", Usage: "rollback COMMIT-OR-TAG", Short: "restore a previous commit", Run: cmdRollback},
		{Name: "remote", Usage: "remote add NAME URL", Short: "manage remote stores", Run: cmdRemote},
		{Name: "push", Usage: "push [REMOTE] [TAG]", Short: "push commits to a remote store", Run: cmdPush},
		{Name: "pull", Usage: "pull [REMOTE] [TAG]", Short: "pull commits from a remote store", Run: cmdPull},
		{Name: "fetch", Usage: "fetch [REMOTE] [REF]", Short: "fetch remote objects without applying", Run: cmdFetch},
		{Name: "checkpoint", Usage: "checkpoint export|import|list", Short: "freeze, export or import the writable layer", Run: cmdCheckpoint},
		{Name: "doctor", Usage: "doctor", Short: "diagnose the environment", Run: cmdDoctor},
		{Name: "help", Usage: "help [COMMAND]", Short: "show help", Run: cmdHelp},
	}
}

// findCommand resolves a subcommand by name.
func findCommand(name string) *Command {
	for i := range commands {
		if commands[i].Name == name {
			return &commands[i]
		}
	}
	return nil
}

// Run dispatches argv (excluding the program name) to a subcommand.
func Run(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 1 && (argv[0] == "--version" || argv[0] == "-v") {
		fmt.Fprintf(stdout, "worksync %s (built %s)\n", Version, BuildTime)
		return 0
	}
	if len(argv) == 0 {
		printUsage(stderr)
		return 2
	}
	app, err := NewApp(stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "worksync: %v\n", err)
		return 1
	}
	defer app.Close()
	name := argv[0]
	cmd := findCommand(name)
	if cmd == nil {
		fmt.Fprintf(stderr, "worksync: unknown command %q\n", name)
		printUsage(stderr)
		return 2
	}
	args := argv[1:]
	// --json and --debug are honored on every command.
	for _, a := range args {
		switch a {
		case "--json":
			app.JSON = true
		case "--debug":
			app.Debug = true
		}
	}
	if err := cmd.Run(ctx, app, args); err != nil {
		var wb *WbError
		if errors.As(err, &wb) {
			fmt.Fprintf(stderr, "worksync: %s: %v\n", wb.Code, wb.Message)
		} else if errors.Is(err, ErrNotImplemented) {
			fmt.Fprintf(stderr, "worksync: %s: not implemented in this build\n", name)
		} else {
			fmt.Fprintf(stderr, "worksync: %v\n", err)
		}
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "worksync - portable development environments\n\nUsage:\n  worksync <command> [options]\n\nCommands:\n")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-10s %s\n", c.Name, c.Short)
	}
}

// resolveBackend selects the backend from manifest + flag (design §7.1).
func resolveBackend(proj *project.Project, flagBackend string) (backend.Backend, error) {
	selector := flagBackend
	if selector == "" {
		selector = proj.Manifest.DefaultBackend()
	}
	b, err := backend.AutoDetect(selector)
	if err != nil {
		return backend.Backend{}, err
	}
	return b, nil
}

// writeJSON prints v as indented JSON when app.JSON, else falls back to the
// provided human renderer.
func writeJSON(app *App, v interface{}, human func() error) error {
	if app.JSON {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(app.Stdout, string(b))
		return nil
	}
	return human()
}

// requireTool checks a binary exists on PATH (design §23 doctor).
func requireTool(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return &WbError{Code: CodeMissingTool, Message: fmt.Sprintf("%s not found on PATH; install %s to use this feature", name, name)}
	}
	return nil
}

var _ = sort.Strings
var _ = filepath.Join
var _ = strings.TrimSpace
var _ = os.Getenv
