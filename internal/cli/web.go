package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/robertolupi/bottown/internal/hostctl"
	"github.com/robertolupi/bottown/internal/server"
	"github.com/robertolupi/bottown/internal/webui"
	"github.com/spf13/cobra"
)

func newWeb(g *globals) *cobra.Command {
	web := &cobra.Command{
		Use:   "web",
		Short: "Open an instance's web interfaces in the browser, signed in as the owner",
	}
	var printOnly, reset bool
	forgejoCmd := &cobra.Command{
		Use:   "forgejo",
		Short: "Open Forgejo signed in as the owner (the current repo's page inside a hosted checkout)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			next := "/"
			if repo, ok := repoOnInstance(".", inst); ok {
				next = "/" + repo
			}
			res, err := webSession(cmd.Context(), g, server.WebSessionRequest{Target: "forgejo", Next: next, ResetPassword: reset})
			if err != nil {
				return err
			}
			if res.NewPassword != "" {
				sec, err := g.secrets()
				if err != nil {
					return err
				}
				if err := sec.Set(hostctl.AccountPassword, res.NewPassword); err != nil {
					return err
				}
				fmt.Fprintln(cmd.ErrOrStderr(), "owner password reset and stored")
			}
			return openLogin(cmd, inst.ServerURL()+res.LoginPath, inst.ForgejoURL()+next, printOnly)
		},
	}
	forgejoCmd.Flags().BoolVar(&reset, "reset-password", false, "set a new owner password first (if it was changed in Forgejo)")
	var port int
	uiCmd := &cobra.Command{
		Use:   "ui",
		Short: "Start the local web UI for this checkout and open it in the browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			top, err := mainCheckout()
			if err != nil {
				return err
			}
			repo, ok := repoOnInstance(top, inst)
			if !ok {
				return fmt.Errorf("this checkout is not hosted on instance %s; run `bottown -i %s repo add` first", inst.Name, inst.Name)
			}
			key, err := ownerKey(g)
			if err != nil {
				return err
			}
			srv, err := webui.NewServer(webui.Config{
				Instance:       inst,
				RepoTop:        top,
				FullRepo:       repo,
				OwnerKey:       key,
				Port:           port,
				CLIVersion:     Version,
				BottownPath:    selfPath(),
				HarnessChecker: checkHarnessCurrent,
			})
			if err != nil {
				return err
			}
			defer srv.Close()

			loginURL := srv.LoginURL()
			if err := openLogin(cmd, loginURL, srv.BaseURL(), printOnly); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "bottown web ui listening on %s (press Ctrl+C to stop)\n", srv.BaseURL())
			return srv.Serve(cmd.Context())
		},
	}
	uiCmd.Flags().IntVar(&port, "port", 0, "port to listen on (default: random free port)")
	healthCmd := &cobra.Command{
		Use:   "health",
		Short: "Open the instance's container health page signed in as the owner",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := g.loadInstance()
			if err != nil {
				return err
			}
			res, err := webSession(cmd.Context(), g, server.WebSessionRequest{Target: "ui"})
			if err != nil {
				return err
			}
			return openLogin(cmd, inst.ServerURL()+res.LoginPath, inst.ServerURL()+"/", printOnly)
		},
	}
	for _, c := range []*cobra.Command{forgejoCmd, uiCmd, healthCmd} {
		c.Flags().BoolVar(&printOnly, "print", false, "print the one-time login URL instead of opening a browser")
	}
	web.AddCommand(forgejoCmd, uiCmd, healthCmd, newWebPreview(), newWebShot())
	return web
}

// The environment a detached preview's child runs with. The child marks
// itself so it does not detach again, and carries the state path so parent
// and child agree on where to record the server.
const (
	previewChildEnv = "BOTTOWN_PREVIEW_CHILD"
	previewStateEnv = "BOTTOWN_PREVIEW_STATE"
)

// previewTTL is how long a detached preview lives before it exits on its
// own, so an orphaned one cannot serve forever.
const previewTTL = time.Hour

// previewSettings holds web preview's flags.
type previewSettings struct {
	port    int
	fixture string
	watch   bool
	detach  bool
	stop    bool
	ttl     time.Duration
}

func newWebPreview() *cobra.Command {
	var s previewSettings
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Serve this checkout's web UI with fixture data, without an instance (works in run worktrees)",
		Long: `Build the web UI in this checkout's web/ and serve it on localhost, with
its API answered from web/fixtures/<fixture>.json instead of an instance.
It needs no sign-in, never contacts prod or dev, and works in run and
actor worktrees: it is how a run looks at the UI it changed.

It prints the URL and, by default, serves until interrupted, which blocks
the shell that started it. --detach instead serves it in the background:
it prints the URL and returns, so a script or an agent keeps its prompt.
Stop a detached one with --stop.

While it runs:

  GET  /api/preview                     the fixture in use, and the others
  POST /api/preview/load                reload it from disk, or {"fixture": NAME}
  PUT  /api/preview/fixture/SECTION     replace a section (status, inbox, live_runs, …)
  POST /api/preview/logs/RUN            append {"lines": [{"kind": "text", "text": "…"}]}
                                        to a run's log and live tail

The controls take the same Origin check as the UI's actions: send
-H "Origin: <the URL>" with curl.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"agent": "true"}, // needs no instance
		RunE: func(cmd *cobra.Command, _ []string) error {
			return webPreview(cmd, s)
		},
	}
	cmd.Flags().IntVar(&s.port, "port", 0, "port to listen on (default: random free port)")
	cmd.Flags().StringVar(&s.fixture, "fixture", "default", "the scenario in web/fixtures to serve")
	cmd.Flags().BoolVar(&s.watch, "watch", false, "rebuild the UI when web/src changes (reload the page to see it)")
	cmd.Flags().BoolVar(&s.detach, "detach", false, "serve in the background and return, printing the URL and how to stop it")
	cmd.Flags().BoolVar(&s.stop, "stop", false, "stop a detached preview started from this checkout")
	cmd.Flags().DurationVar(&s.ttl, "ttl", previewTTL, "how long a detached preview lives before it exits (0: until stopped)")
	return cmd
}

// webPreview resolves the checkout and dispatches to stopping, the detached
// child, detaching, or the foreground server.
func webPreview(cmd *cobra.Command, s previewSettings) error {
	if s.detach && s.stop {
		return errors.New("--detach and --stop are mutually exclusive")
	}
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("not inside a git checkout")
	}
	webDir := filepath.Join(top, "web")
	if _, err := os.Stat(filepath.Join(webDir, "package.json")); err != nil {
		return fmt.Errorf("no web UI in %s: web/package.json is missing", top)
	}
	fixtures := filepath.Join(webDir, "fixtures")
	state := previewStatePath(os.Getenv(previewStateEnv), top)
	switch {
	case s.stop:
		return stopPreview(cmd, state)
	case os.Getenv(previewChildEnv) == "1":
		return runPreview(cmd, webDir, fixtures, state, s)
	}
	// Fail on a bad fixture before the build, and before starting a
	// detached child that would otherwise report it only from the log,
	// after the build.
	if _, err := webui.LoadFixture(fixtures, s.fixture); err != nil {
		return err
	}
	if s.detach {
		return detachPreview(cmd, top, state, s)
	}
	return runPreview(cmd, webDir, fixtures, "", s)
}

// runPreview builds the checkout's web UI into a temporary directory and
// serves it with a webui.PreviewServer. A non-empty state makes it a
// detached child: it records where it serves, ends after the TTL, and
// removes the record when it exits.
func runPreview(cmd *cobra.Command, webDir, fixtures, state string, s previewSettings) error {
	ctx, errOut := cmd.Context(), cmd.ErrOrStderr()
	if state != "" {
		// The child outlives the shell that started it: a TTL and signals
		// are what end it, so cleanup always runs.
		if s.ttl > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, s.ttl)
			defer cancel()
		}
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	if err := npmInstalled(ctx, webDir, errOut); err != nil {
		return err
	}
	out, err := os.MkdirTemp("", "bottown-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(out)

	if !s.watch {
		if err := viteBuild(ctx, webDir, out, errOut); err != nil {
			return err
		}
	} else {
		// vite itself rather than npm run, so that cancelling ctx stops it.
		vite := exec.CommandContext(ctx, filepath.Join(webDir, "node_modules", ".bin", "vite"), "build", "--outDir", out, "--emptyOutDir", "--watch")
		vite.Dir, vite.Stdout, vite.Stderr = webDir, errOut, errOut
		if err := vite.Start(); err != nil {
			return fmt.Errorf("build the web UI: %w", err)
		}
		exited := make(chan struct{})
		go func() { _ = vite.Wait(); close(exited) }()
		defer func() {
			_ = vite.Process.Kill()
			<-exited
		}()
		if err := waitForBuild(ctx, filepath.Join(out, "index.html"), exited); err != nil {
			return err
		}
	}

	srv, err := webui.NewPreviewServer(webui.PreviewConfig{
		Port:        s.port,
		UI:          os.DirFS(out),
		FixturesDir: fixtures,
		Fixture:     s.fixture,
	})
	if err != nil {
		return err
	}
	defer srv.Close()
	if state != "" {
		st := previewState{PID: os.Getpid(), URL: srv.BaseURL(), Started: time.Now(), Log: state + ".log"}
		if err := savePreviewState(state, st); err != nil {
			return err
		}
		// A clean exit (TTL or --stop) leaves nothing behind; a crash keeps
		// the log for a postmortem.
		defer func() {
			_ = os.Remove(state)
			_ = os.Remove(st.Log)
		}()
	}
	fmt.Fprintf(errOut, "bottown web preview of %s, fixture %s (press Ctrl+C to stop):\n", webDir, s.fixture)
	fmt.Fprintln(cmd.OutOrStdout(), srv.BaseURL()+"/")
	return srv.Serve(ctx)
}

// previewState is where a detached preview records where it serves, so
// `--stop` can find it.
type previewState struct {
	PID     int       `json:"pid"`
	URL     string    `json:"url"`
	Started time.Time `json:"started"`
	Log     string    `json:"log"`
}

// previewStatePath returns where a checkout's detached preview records
// itself: the child's own path when it was handed one, else a temp file
// named for the checkout so two worktrees do not collide.
func previewStatePath(fromEnv, top string) string {
	if fromEnv != "" {
		return fromEnv
	}
	sum := sha256.Sum256([]byte(top))
	return filepath.Join(os.TempDir(), fmt.Sprintf("bottown-preview-%x.json", sum[:6]))
}

func savePreviewState(path string, st previewState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func loadPreviewState(path string) (previewState, error) {
	var st previewState
	b, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// detachPreview starts the server as a child in its own session and waits
// until it serves, then prints its URL and how to stop it.
func detachPreview(cmd *cobra.Command, top, state string, s previewSettings) error {
	// One detach at a time per checkout: without the lock, two started at
	// once would both pass the "already running" check below.
	unlock, err := lockPreview(state)
	if err != nil {
		return err
	}
	defer unlock()
	if st, err := loadPreviewState(state); err == nil && previewAlive(st) {
		return fmt.Errorf("a preview is already running at %s: stop it with `%s web preview --stop`", st.URL, selfPath())
	}
	_ = os.Remove(state)
	logPath := state + ".log"
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()

	args := []string{"web", "preview",
		"--port", strconv.Itoa(s.port),
		"--fixture", s.fixture,
		"--ttl", s.ttl.String(),
	}
	if s.watch {
		args = append(args, "--watch")
	}
	child := exec.Command(selfPath(), args...)
	child.Dir = top
	child.Env = append(os.Environ(), previewChildEnv+"=1", previewStateEnv+"="+state)
	child.Stdout, child.Stderr = logf, logf
	// Its own session, so it outlives this shell and the terminal closing.
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	pid := child.Process.Pid
	_ = child.Process.Release()

	fmt.Fprintf(cmd.ErrOrStderr(), "building the web UI in the background (log: %s)\n", logPath)
	st, err := awaitPreview(cmd.Context(), state, pid, logPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), st.URL+"/")
	fmt.Fprintf(cmd.ErrOrStderr(), "preview %d serving with fixture %s (ttl %s); stop it with `%s web preview --stop`\n", st.PID, s.fixture, s.ttl, selfPath())
	return nil
}

// awaitPreview waits for a detached child to record itself, or to exit.
func awaitPreview(ctx context.Context, state string, pid int, logPath string) (previewState, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if st, err := loadPreviewState(state); err == nil {
			return st, nil
		}
		if !processAlive(pid) {
			return previewState{}, fmt.Errorf("the preview exited before it served:\n%s", tailFile(logPath, 20))
		}
		if time.Now().After(deadline) {
			return previewState{}, errors.New("the preview did not start within 10 minutes")
		}
		select {
		case <-ctx.Done():
			return previewState{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// stopPreview ends a detached preview started from this checkout.
func stopPreview(cmd *cobra.Command, state string) error {
	st, err := loadPreviewState(state)
	if err != nil {
		return errors.New("no detached preview from this checkout")
	}
	stopped, err := endPreviewState(st, state)
	if err != nil {
		return err
	}
	if stopped {
		fmt.Fprintf(cmd.OutOrStdout(), "stopped preview %d (%s)\n", st.PID, st.URL)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "preview %d was not running\n", st.PID)
	}
	return nil
}

// endPreviewState stops the preview the state file names, if it is still
// the one serving st.URL, and removes the state and log files. It reports
// whether a running preview was stopped. It signals only a pid that the
// probe confirms is still the preview, so a reused pid is left alone.
func endPreviewState(st previewState, state string) (bool, error) {
	cleanup := func() {
		_ = os.Remove(state)
		if st.Log != "" {
			_ = os.Remove(st.Log)
		}
	}
	if !previewAlive(st) {
		cleanup()
		return false, nil
	}
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return false, fmt.Errorf("stop preview %d: %w", st.PID, err)
	}
	for deadline := time.Now().Add(5 * time.Second); processAlive(st.PID) && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(st.PID) {
		return false, fmt.Errorf("preview %d did not stop; kill it with `kill %d`", st.PID, st.PID)
	}
	// The child removes its own files on exit; remove them too, in case it
	// crashed.
	cleanup()
	return true, nil
}

// stopRunPreviews stops any detached preview a run recorded under dir, so
// a run's preview does not outlive the run when its scratch directory goes.
func stopRunPreviews(w io.Writer, dir string) {
	if dir == "" {
		return
	}
	states, _ := filepath.Glob(filepath.Join(dir, "bottown-preview-*.json"))
	for _, state := range states {
		st, err := loadPreviewState(state)
		if err != nil {
			continue
		}
		if _, err := endPreviewState(st, state); err != nil {
			fmt.Fprintf(w, "warning: stop preview %d: %v\n", st.PID, err)
		}
	}
}

// previewAlive reports whether the state file still names a running
// preview: the process it names is alive and still serves st.URL. A pid
// alone is not enough, since the state file may outlive its preview and
// the pid may now belong to an unrelated process.
func previewAlive(st previewState) bool {
	if !processAlive(st.PID) {
		return false
	}
	pid, ok := probePreviewPID(st.URL)
	return ok && pid == st.PID
}

// probePreviewPID asks the server at base for the pid it reports, which
// identifies the preview answering there.
func probePreviewPID(base string) (int, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(base, "/") + "/api/preview")
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var info struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&info); err != nil {
		return 0, false
	}
	return info.PID, info.PID > 0
}

// lockPreview takes an exclusive lock for a checkout's preview, so two
// --detach started at once cannot both pass the "already running" check.
// It fails at once when another detach holds the lock.
func lockPreview(state string) (func(), error) {
	f, err := os.OpenFile(state+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("another preview is starting from this checkout; try again")
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// processAlive reports whether a process with pid exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// tailFile returns the last n lines of a file, for reporting a failed
// background start.
func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// npmInstalled runs npm ci in webDir when node_modules is missing or older
// than package-lock.json, as the Makefile's web/node_modules target does.
func npmInstalled(ctx context.Context, webDir string, errOut io.Writer) error {
	mods, err := os.Stat(filepath.Join(webDir, "node_modules"))
	lock, lerr := os.Stat(filepath.Join(webDir, "package-lock.json"))
	if err == nil && (lerr != nil || !lock.ModTime().After(mods.ModTime())) {
		return nil
	}
	npm := exec.CommandContext(ctx, "npm", "ci")
	npm.Dir, npm.Stdout, npm.Stderr = webDir, errOut, errOut
	if err := npm.Run(); err != nil {
		return fmt.Errorf("npm ci in %s (needs Node and npm): %w", webDir, err)
	}
	now := time.Now()
	return os.Chtimes(filepath.Join(webDir, "node_modules"), now, now)
}

// waitForBuild waits for vite --watch's first build to write index.html.
func waitForBuild(ctx context.Context, index string, exited <-chan struct{}) error {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(index); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return errors.New("build the web UI: vite exited before its first build")
		case <-tick.C:
		}
	}
}

func webSession(ctx context.Context, g *globals, body server.WebSessionRequest) (server.WebSessionResponse, error) {
	var res server.WebSessionResponse
	inst, err := g.loadInstance()
	if err != nil {
		return res, err
	}
	key, err := ownerKey(g)
	if err != nil {
		return res, err
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, inst.ServerURL()+"/v1/web/session", bytes.NewReader(b))
	if err != nil {
		return res, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return res, fmt.Errorf("instance %s unreachable (is it up?): %w", inst.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		var e struct{ Error string }
		if json.Unmarshal(msg, &e) == nil && e.Error != "" {
			return res, fmt.Errorf("%s: %s", inst.Name, e.Error)
		}
		return res, fmt.Errorf("%s: %s %s", inst.Name, resp.Status, strings.TrimSpace(string(msg)))
	}
	return res, json.NewDecoder(resp.Body).Decode(&res)
}

func openLogin(cmd *cobra.Command, loginURL, landing string, printOnly bool) error {
	if printOnly {
		fmt.Fprintln(cmd.OutOrStdout(), loginURL)
		return nil
	}
	if err := openBrowser(loginURL); err != nil {
		return fmt.Errorf("open browser: %w (use --print to get the one-time URL)", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "opened %s\n", landing)
	return nil
}

// openBrowser opens url in the platform's default browser.
func openBrowser(url string) error {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	return c.Run()
}
