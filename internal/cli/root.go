// Package cli implements the bottown command: host commands that manage an
// instance through Docker, and container commands (serve, admin) that run
// inside it.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/flow"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/spf13/cobra"
)

// Version is set at build time with -ldflags -X.
var Version = "dev"

type globals struct {
	instance string
	source   string // how instance was chosen, for messages
}

// ExitAgentDidNothing is bottown's exit status for a flow that stopped
// because a run's agent made no tool call (flow.AgentDidNothing), so a
// process that runs flows can tell an unavailable actor from other stops.
const ExitAgentDidNothing = 3

// Execute runs the bottown command.
func Execute(ctx context.Context) int {
	if err := newRoot().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "bottown:", err)
		return exitCode(err)
	}
	return 0
}

// exitCode is the exit status for a command's error: 1, or
// ExitAgentDidNothing for that flow stop, and for a milestone run that
// stopped short having removed an agent that did nothing from its pool.
func exitCode(err error) int {
	var stop *flow.StopError
	if errors.As(err, &stop) && stop.Result.Reason == flow.AgentDidNothing {
		return ExitAgentDidNothing
	}
	var unfinished *milestone.UnfinishedError
	if errors.As(err, &unfinished) && unfinished.AgentDidNothing() {
		return ExitAgentDidNothing
	}
	return 1
}

// agentCmds marks the agent CLI commands, which act for the run or actor
// worktree they are run in rather than for an instance.
func agentCmds(cmds ...*cobra.Command) []*cobra.Command {
	for _, c := range cmds {
		c.Annotations = map[string]string{"agent": "true"}
	}
	return cmds
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
					// Agent commands talk to the server their worktree or
					// run points at; they do not need an instance.
					if cmd.Annotations["agent"] == "true" || cmd.Parent() != nil && cmd.Parent().Annotations["agent"] == "true" {
						return nil
					}
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
		newRepo(g), newCredential(g), newWeb(g), newRun(g), newFlow(g), newMergeAll(g), newDebate(g), newModels(g), newMCP(g), newActor(g), newHarness(), newClone(g), newGit(g), newGuide(),
		newServe(), newAdmin(),
		&cobra.Command{
			Use:   "version",
			Short: "Print the bottown version",
			Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), Version) },
		},
	)
	root.AddCommand(agentCmds(newWhoami(), newIssue(), newPR(), newComment(), newReview(), newAPI(), newCheck(), newMilestone(g), newWiki(g))...)
	return root
}
