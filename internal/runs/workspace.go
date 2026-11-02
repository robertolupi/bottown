// Package runs is the host side of agent runs: a bottown-owned bare mirror
// and one git worktree per run under <repo>/.bottown/, the run's local
// record, its prompt and harness launch.
//
// Layout:
//
//	<repo>/.bottown/mirror.git          bare mirror of the bottown remote
//	<repo>/.bottown/runs/<id>/          the run's worktree (the agent's cwd)
//	<repo>/.bottown/runs/<id>.json      local record (0600; holds the key while running)
//	<repo>/.bottown/runs/<id>.log       harness output
//	<repo>/.bottown/runs/<id>.mcp.json  harness MCP config (0600)
//	<repo>/.bottown/runs/<id>.prompt.md the goal prompt
//	<repo>/.bottown/runs/<id>.attachments/ the issue's attachments, while the run lasts
//	<launcher temp>/bottown-<workspace hash>-<id>/ the run's scratch directory
//
// Per-run files sit next to the worktree, never inside it, so an agent's
// `git add -A` cannot commit them. Runs never touch the user's checkout:
// the mirror has its own config, refs and objects.
package runs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/robertolupi/bottown/internal/config"
)

// Workspace is the .bottown directory of one hosted checkout.
type Workspace struct {
	Top       string // the user's checkout
	Dir       string // Top/.bottown
	Mirror    string // Dir/mirror.git
	RunsDir   string // Dir/runs
	RemoteURL string // the bottown git URL of the repo
	// OwnerAuth are `git -c` settings that authenticate mirror fetches as
	// the owner; they are passed per command, never stored in the mirror.
	OwnerAuth []string
}

// Open prepares top/.bottown and excludes it from the checkout's status.
func Open(top, remoteURL string, ownerAuth []string) (*Workspace, error) {
	w := &Workspace{
		Top:       top,
		Dir:       filepath.Join(top, ".bottown"),
		RemoteURL: remoteURL,
		OwnerAuth: ownerAuth,
	}
	w.Mirror = filepath.Join(w.Dir, "mirror.git")
	w.RunsDir = filepath.Join(w.Dir, "runs")
	if err := os.MkdirAll(w.RunsDir, 0o700); err != nil {
		return nil, err
	}
	return w, w.exclude()
}

// exclude adds .bottown/ to the checkout's info/exclude, so no .gitignore
// change is needed.
func (w *Workspace) exclude() error {
	p, err := Git(w.Top, nil, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.Top, p)
	}
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "/.bottown/" {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := ""
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		prefix = "\n"
	}
	_, err = fmt.Fprintf(f, "%s# bottown runs and mirror\n/.bottown/\n", prefix)
	return err
}

func appendLine(path, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		line = "\n" + line
	}
	_, err = fmt.Fprintln(f, line)
	return err
}

// Sync creates the mirror if needed and fetches it.
func (w *Workspace) Sync(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(w.Mirror, "HEAD")); errors.Is(err, os.ErrNotExist) {
		if _, err := Git(w.Dir, nil, "init", "-q", "--bare", w.Mirror); err != nil {
			return err
		}
		for _, kv := range [][]string{
			{"remote.origin.url", w.RemoteURL},
			{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
			// Each run worktree gets its own config (identity, credentials).
			{"extensions.worktreeConfig", "true"},
		} {
			if _, err := Git(w.Mirror, nil, "config", kv[0], kv[1]); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	}
	// With worktreeConfig, a shared core.bare=true would make every run
	// worktree think it is bare; it belongs in the mirror's own
	// config.worktree (git-worktree(1), CONFIGURATION FILE).
	if v, err := Git(w.Mirror, nil, "config", "--local", "--get", "core.bare"); err == nil && v == "true" {
		if _, err := Git(w.Mirror, nil, "config", "--local", "--unset", "core.bare"); err != nil {
			return err
		}
		if _, err := Git(w.Mirror, nil, "config", "--worktree", "core.bare", "true"); err != nil {
			return err
		}
	}
	if _, err := Git(w.Mirror, nil, "config", "remote.origin.url", w.RemoteURL); err != nil {
		return err
	}
	// Files bottown writes into run worktrees for harnesses (agy's workspace
	// MCP config holds the run key) must never be committed by the agent.
	for _, p := range []string{AgyMCPPath, KimiMCPPath, CodexMCPPath, CrushMCPPath} {
		if err := appendLine(filepath.Join(w.Mirror, "info", "exclude"), "/"+p); err != nil {
			return err
		}
	}
	if err := appendLine(filepath.Join(w.Mirror, "info", "exclude"), "/.crush/"); err != nil {
		return err
	}
	_, err := GitCtx(ctx, w.Mirror, w.OwnerAuth, "fetch", "-q", "--prune", "origin")
	return err
}

// DefaultBranch is the bottown repo's default branch.
func (w *Workspace) DefaultBranch(ctx context.Context) (string, error) {
	out, err := GitCtx(ctx, w.Mirror, w.OwnerAuth, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			return strings.Fields(ref)[0], nil
		}
	}
	return "", errors.New("cannot determine the default branch of the bottown remote")
}

// WorktreeDir is where run id's worktree lives.
func (w *Workspace) WorktreeDir(id string) string { return filepath.Join(w.RunsDir, id) }

// TmpDir is where launched run id keeps its scratch directory. Use the
// launcher's temporary directory rather than nesting it in the owner's checkout:
// tests launched by the harness may need temporary directories outside git.
// Include the workspace in the name to isolate runs from different checkouts.
func (w *Workspace) TmpDir(id string) string {
	sum := sha256.Sum256([]byte(w.RunsDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("bottown-%x-%s", sum[:8], id))
}

// AddImplement adds a worktree on branch, resuming origin/<branch> if it
// exists (a later run continues the same branch) or starting it from
// origin/<base>.
func (w *Workspace) AddImplement(id, branch, base string) (string, error) {
	dir := w.WorktreeDir(id)
	start := "origin/" + base
	if _, err := Git(w.Mirror, nil, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch); err == nil {
		start = "origin/" + branch
	}
	if _, err := Git(w.Mirror, nil, "worktree", "add", "-q", "-B", branch, dir, start); err != nil {
		return "", err
	}
	for _, kv := range [][]string{
		{"branch." + branch + ".remote", "origin"},
		{"branch." + branch + ".merge", "refs/heads/" + branch},
	} {
		if _, err := Git(dir, nil, "config", kv[0], kv[1]); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// AddVerify adds a worktree detached at sha.
func (w *Workspace) AddVerify(id, sha string) (string, error) {
	dir := w.WorktreeDir(id)
	_, err := Git(w.Mirror, nil, "worktree", "add", "-q", "--detach", dir, sha)
	return dir, err
}

// Configure gives a worktree the actor's identity and a credential helper
// that hands git the run key, replacing any inherited helper.
func Configure(dir, actor, credentialHelper string) error {
	for _, kv := range [][]string{
		{"user.name", actor},
		{"user.email", actor + "@bottown.local"},
		{"push.default", "current"},
	} {
		if _, err := Git(dir, nil, "config", "--worktree", kv[0], kv[1]); err != nil {
			return err
		}
	}
	// An empty value resets helpers from global config (osxkeychain, gh).
	if _, err := Git(dir, nil, "config", "--worktree", "credential.helper", ""); err != nil {
		return err
	}
	_, err := Git(dir, nil, "config", "--worktree", "--add", "credential.helper", credentialHelper)
	return err
}

// Remove deletes a run's worktree.
func (w *Workspace) Remove(id string) error {
	dir := w.WorktreeDir(id)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		_, err := Git(w.Mirror, nil, "worktree", "prune")
		return err
	}
	_, err := Git(w.Mirror, nil, "worktree", "remove", "--force", dir)
	return err
}

// RepoSettingsAt reads config.RepoSettingsFile at ref (e.g. origin/main) in
// the git repository dir; a ref without the file gets the defaults.
func RepoSettingsAt(dir, ref string) (config.RepoSettings, error) {
	if out, err := Git(dir, nil, "ls-tree", "--name-only", ref, "--", config.RepoSettingsFile); err != nil {
		return config.RepoSettings{}, err
	} else if out == "" {
		return config.DefaultRepoSettings(), nil
	}
	b, err := Git(dir, nil, "show", ref+":"+config.RepoSettingsFile)
	if err != nil {
		return config.RepoSettings{}, err
	}
	s, err := config.ParseRepoSettings([]byte(b))
	if err != nil {
		return config.RepoSettings{}, fmt.Errorf("%s at %s: %w", config.RepoSettingsFile, ref, err)
	}
	return s, nil
}

// Git runs git in dir with extra `-c` settings and returns trimmed stdout.
func Git(dir string, cfg []string, args ...string) (string, error) {
	return GitCtx(context.Background(), dir, cfg, args...)
}

// GitCtx is Git with a context.
func GitCtx(ctx context.Context, dir string, cfg []string, args ...string) (string, error) {
	all := append([]string{"-C", dir}, cfg...)
	cmd := exec.CommandContext(ctx, "git", append(all, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
