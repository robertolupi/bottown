// Package cli implements the bottown command: host commands that manage an
// instance through Docker, and container commands (serve, admin) that run
// inside it.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/spf13/cobra"
)

// Version is set at build time with -ldflags -X.
var Version = "dev"

type globals struct {
	instance string
	source   string // how instance was chosen, for messages
}

// Execute runs the bottown command.
func Execute(ctx context.Context) int {
	if err := newRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "bottown:", err)
		return 1
	}
	return 0
}

func newRoot() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:           "bottown",
		Short:         "A self-contained Forgejo control plane for coding agents",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case cmd.Flags().Changed("instance"):
				g.source = "--instance"
			case os.Getenv("BOTTOWN_INSTANCE") != "":
				g.source = "$BOTTOWN_INSTANCE"
			default:
				// Infer from the checkout the command runs in.
				name, source, err := resolveInstance(".")
				if err != nil {
					return err
				}
				g.instance, g.source = name, source
			}
			return config.ValidInstance(g.instance)
		},
	}
	def := os.Getenv("BOTTOWN_INSTANCE")
	if def == "" {
		def = config.DefaultInstance
	}
	root.PersistentFlags().StringVarP(&g.instance, "instance", "i", def,
		"instance name ($BOTTOWN_INSTANCE; otherwise inferred from the checkout's git config bottown.instance or remotes)")
	root.AddCommand(
		newUp(g), newDown(g), newStatus(g), newLogs(g), newImage(g),
		newRepo(g), newCredential(g), newWeb(g), newRun(g), newMCP(g), newActor(g),
		newServe(), newAdmin(),
		&cobra.Command{
			Use:   "version",
			Short: "Print the bottown version",
			Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), Version) },
		},
	)
	return root
}
