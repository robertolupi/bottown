package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	web.AddCommand(forgejoCmd, uiCmd, healthCmd, newWebPreview())
	return web
}

func newWebPreview() *cobra.Command {
	var port int
	var fixture string
	var watch bool
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Serve this checkout's web UI with fixture data, without an instance (works in run worktrees)",
		Long: `Build the web UI in this checkout's web/ and serve it on localhost, with
its API answered from web/fixtures/<fixture>.json instead of an instance.
It needs no sign-in, never contacts prod or dev, and works in run and
actor worktrees: it is how a run looks at the UI it changed.

It prints the URL and serves until interrupted. While it runs:

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
			return webPreview(cmd, port, fixture, watch)
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "port to listen on (default: random free port)")
	cmd.Flags().StringVar(&fixture, "fixture", "default", "the scenario in web/fixtures to serve")
	cmd.Flags().BoolVar(&watch, "watch", false, "rebuild the UI when web/src changes (reload the page to see it)")
	return cmd
}

// webPreview builds the checkout's web UI into a temporary directory and
// serves it with a webui.PreviewServer.
func webPreview(cmd *cobra.Command, port int, fixture string, watch bool) error {
	ctx, errOut := cmd.Context(), cmd.ErrOrStderr()
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("not inside a git checkout")
	}
	webDir := filepath.Join(top, "web")
	if _, err := os.Stat(filepath.Join(webDir, "package.json")); err != nil {
		return fmt.Errorf("no web UI in %s: web/package.json is missing", top)
	}
	fixtures := filepath.Join(webDir, "fixtures")
	// Fail on a bad fixture before the build.
	if _, err := webui.LoadFixture(fixtures, fixture); err != nil {
		return err
	}
	if err := npmInstalled(ctx, webDir, errOut); err != nil {
		return err
	}
	out, err := os.MkdirTemp("", "bottown-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(out)

	// vite itself rather than npm run, so that cancelling ctx stops it.
	args := []string{"build", "--outDir", out, "--emptyOutDir"}
	if watch {
		args = append(args, "--watch")
	}
	vite := exec.CommandContext(ctx, filepath.Join(webDir, "node_modules", ".bin", "vite"), args...)
	vite.Dir, vite.Stdout, vite.Stderr = webDir, errOut, errOut
	if !watch {
		if err := vite.Run(); err != nil {
			return fmt.Errorf("build the web UI: %w", err)
		}
	} else {
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
		Port:        port,
		UI:          os.DirFS(out),
		FixturesDir: fixtures,
		Fixture:     fixture,
	})
	if err != nil {
		return err
	}
	defer srv.Close()
	fmt.Fprintf(errOut, "bottown web preview of %s, fixture %s (press Ctrl+C to stop):\n", webDir, fixture)
	fmt.Fprintln(cmd.OutOrStdout(), srv.BaseURL()+"/")
	return srv.Serve(ctx)
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
