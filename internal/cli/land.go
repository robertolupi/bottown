package cli

import (
	"fmt"
	"net/url"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/land"
	"github.com/robertolupi/bottown/internal/server"
	"github.com/spf13/cobra"
)

// newLand is the UI's background worker; Main runs both check groups.
func newLand(g *globals) *cobra.Command {
	var head string
	cmd := &cobra.Command{Use: "land PR", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		n, err := parseNumber(args[0])
		if err != nil {
			return err
		}
		rc, err := openRunContext(g)
		if err != nil {
			return err
		}
		api := client.New(rc.inst.ServerURL(), rc.key)
		var pr server.PROut
		if err := api.Call(cmd.Context(), "GET", fmt.Sprintf("/v1/forge/pr?repo=%s&number=%d", url.QueryEscape(rc.repo), n), nil, &pr); err != nil {
			return err
		}
		if pr.HeadSHA != head {
			return fmt.Errorf("PR head moved; reload and try again")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Landing !%d: checking the combined tree before merge\n", n)
		outcome := land.Land(cmd.Context(), api, flowChecker{ws: rc.ws}, land.Policy{Repo: rc.repo, RunsDir: rc.ws.RunsDir, Kind: land.Main, CheckTimeout: defaultCheckTimeout}, pr)
		fmt.Fprintln(cmd.OutOrStdout(), outcome)
		if _, ok := outcome.(land.Landed); !ok {
			return fmt.Errorf("landing stopped: %s", outcome)
		}
		return nil
	}}
	cmd.Flags().StringVar(&head, "head-sha", "", "Expected PR head")
	return cmd
}
