package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

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
				Instance:   inst,
				RepoTop:    top,
				FullRepo:   repo,
				OwnerKey:   key,
				Port:       port,
				CLIVersion: Version,
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
	web.AddCommand(forgejoCmd, uiCmd, healthCmd)
	return web
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
