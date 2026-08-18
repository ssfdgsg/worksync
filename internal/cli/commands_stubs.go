package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"worksync/internal/project"
	"worksync/internal/transport/sshurl"
)

// notImplemented is shared by command stubs that will be implemented in
// later milestones (M2 lifecycle, M4 commit, M5 transport).
func notImplemented(app *App, name string) error {
	return fmt.Errorf("%w: %s is registered in the v0 CLI surface", ErrNotImplemented, name)
}

// remoteRegistryFile returns the per-project file of dynamically-added
// remotes (design §16; manifest remotes remain the source of truth, this
// file is like git config for convenience).
func (a *App) remoteRegistryFile(projectID string) string {
	return filepath.Join(a.Layout.ProjectsDir, projectID, "remotes.json")
}

// StoredRemote is one dynamically-registered remote.
type StoredRemote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func loadRemotes(app *App, projectID string) (map[string]StoredRemote, error) {
	out := map[string]StoredRemote{}
	b, err := os.ReadFile(app.remoteRegistryFile(projectID))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse remotes: %w", err)
	}
	return out, nil
}

func saveRemotes(app *App, projectID string, remotes map[string]StoredRemote) error {
	dir := filepath.Dir(app.remoteRegistryFile(projectID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(remotes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(app.remoteRegistryFile(projectID), b, 0o644)
}

func cmdRemote(ctx context.Context, app *App, args []string) error {
	proj, err := project.Load(app.Workdir())
	if err != nil {
		return err
	}
	if len(args) == 0 {
		remotes, err := loadRemotes(app, proj.ID)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(remotes))
		for n := range remotes {
			names = append(names, n)
		}
		sortStrings(names)
		for _, n := range names {
			fmt.Fprintf(app.Stdout, "%s\t%s\n", n, remotes[n].URL)
		}
		return nil
	}
	if args[0] != "add" || len(args) != 3 {
		return &WbError{Code: CodeConfig, Message: "usage: worksync remote add NAME ssh://user@host/~/path"}
	}
	name, rawURL := args[1], args[2]
	if _, err := sshurl.Parse(rawURL); err != nil {
		return &WbError{Code: CodeConfig, Message: fmt.Sprintf("invalid remote url: %v", err)}
	}
	remotes, err := loadRemotes(app, proj.ID)
	if err != nil {
		return err
	}
	remotes[name] = StoredRemote{Name: name, URL: rawURL}
	if err := saveRemotes(app, proj.ID, remotes); err != nil {
		return err
	}
	fmt.Fprintf(app.Stdout, "added remote %s (%s)\n", name, rawURL)
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var _ = strings.TrimSpace
var _ = errors.Is
