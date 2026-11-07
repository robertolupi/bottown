package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// The wiki commands reach a repository's wiki through bottown's wiki store
// (internal/server/wiki.go). In a run or an actor worktree they act as the
// agent; anywhere else on the host, as the owner.

func newWiki(g *globals) *cobra.Command {
	w := &cobra.Command{
		Use:   "wiki",
		Short: "Read and write the repository's wiki through bottown (agent and owner CLI)",
		Long: `Read and write a repository's Forgejo wiki through bottown's wiki store.
Agents get no wiki checkout: Forgejo's wiki API and the wiki's git repository
are closed to them, and the store decides each read and write by role and
path. The wiki is never binding: an issue or a design document is.

Generated blocks, between <!-- bottown:<name>:generated --> and
<!-- /bottown:<name>:generated -->, are bottown's: a put discards whatever
you send inside them and fills them again, so leave them as they are.

In a run or an actor worktree these commands act as the agent; anywhere
else on the host, as the owner (--repo names the repository outside a
checkout).`,
	}
	var repo string
	w.PersistentFlags().StringVar(&repo, "repo", "", "repository (default: the run's, else this checkout's)")

	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List the wiki's files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, r, err := wikiCaller(g, repo)
			if err != nil {
				return err
			}
			var out server.WikiListOut
			if err := c.Call(cmd.Context(), "GET", "/v1/wiki/files?"+repoQuery(r).Encode(), nil, &out); err != nil {
				return err
			}
			if listJSON {
				return printJSON(cmd, out)
			}
			if out.Head == "" {
				fmt.Fprintf(cmd.OutOrStdout(), "the wiki of %s has no pages yet\n", out.Repo)
				return nil
			}
			for _, f := range out.Files {
				fmt.Fprintln(cmd.OutOrStdout(), f.Path)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, "print JSON")

	var readJSON bool
	read := &cobra.Command{
		Use:   "read PATH",
		Short: "Print a wiki file (PATH: a file, or a page's slug); its blob SHA goes to stderr",
		Long: `Print a wiki file to stdout, and to stderr the blob SHA to pass to
put --base. PATH is a file in the wiki (Idea.md, reviews/12-x/r1-claude.md) or
a page's slug (Idea), which also finds a page Forgejo stored as Idea.-.md.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := wikiCaller(g, repo)
			if err != nil {
				return err
			}
			q := repoQuery(r)
			q.Set("path", args[0])
			var out server.WikiReadOut
			if err := c.Call(cmd.Context(), "GET", "/v1/wiki/file?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			if readJSON {
				return printJSON(cmd, out)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%s blob %s (put --base %s)\n", out.Path, out.SHA, out.SHA)
			_, err = io.WriteString(cmd.OutOrStdout(), out.Content)
			return err
		},
	}
	read.Flags().BoolVar(&readJSON, "json", false, "print JSON: path, sha, head and content")

	var base, file, message string
	var putJSON bool
	put := &cobra.Command{
		Use:   "put PATH [--base SHA] [--file F|-] [-m MSG]",
		Short: "Write a wiki file, if it has not changed since you read it",
		Long: `Write a wiki file from --file (default: stdin). The write is a
compare-and-swap: --base is the blob SHA read printed, and the put fails with
a conflict when the file's text has changed since (changes to generated
blocks alone are no conflict): read it again and re-apply your edit.
Without --base the put creates a new file, and fails if it exists.

The commit also regenerates every generated block in the wiki.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				file = "-"
			}
			content, err := readBody(cmd, "", file)
			if err != nil {
				return err
			}
			c, r, err := wikiCaller(g, repo)
			if err != nil {
				return err
			}
			var out server.WikiPutOut
			err = c.Call(cmd.Context(), "PUT", "/v1/wiki/file", server.WikiPutIn{Repo: r, Path: args[0], Content: content, Base: base, Message: message}, &out)
			if errors.Is(err, client.ErrWikiConflict) {
				return fmt.Errorf("%w\n(read the file again with `bottown wiki read %s`, re-apply your edit, and put with its new --base)", err, args[0])
			}
			if err != nil {
				return err
			}
			if putJSON {
				return printJSON(cmd, out)
			}
			if out.Unchanged {
				fmt.Fprintf(cmd.OutOrStdout(), "%s unchanged (blob %s)\n", out.Path, out.SHA)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (blob %s) in %s\n", out.Path, out.SHA, shortSHA(out.Commit))
			if others := without(out.Changed, out.Path); len(others) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "regenerated blocks in: %s\n", strings.Join(others, ", "))
			}
			return nil
		},
	}
	put.Flags().StringVar(&base, "base", "", "the blob SHA you read (from wiki read); without it, create the file")
	put.Flags().StringVar(&file, "file", "", "read the content from a file, or - for stdin (the default)")
	put.Flags().StringVarP(&message, "message", "m", "", "commit message (default: Update <page>)")
	put.Flags().BoolVar(&putJSON, "json", false, "print JSON")

	var check, genJSON bool
	generate := &cobra.Command{
		Use:   "generate [--check]",
		Short: "Regenerate every generated block in the wiki (the owner); --check only reports",
		Long: `Walk the whole wiki and regenerate every stale generated block, in one
commit by bottown; with nothing stale it commits nothing. Every put does
this already: run it after editing pages in Forgejo's web UI. Only the owner
commits; --check, which anyone who can read the wiki may run, writes nothing,
lists the stale blocks and fails when there are any.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, r, err := wikiCaller(g, repo)
			if err != nil {
				return err
			}
			var out server.WikiGenerateOut
			if err := c.Call(cmd.Context(), "POST", "/v1/wiki/generate", server.WikiGenerateIn{Repo: r, Check: check}, &out); err != nil {
				return err
			}
			if genJSON {
				if err := printJSON(cmd, out); err != nil {
					return err
				}
			} else {
				printGenerate(cmd.OutOrStdout(), out, check)
			}
			if check && len(out.Stale) > 0 {
				return fmt.Errorf("%d stale generated blocks in the wiki of %s", len(out.Stale), out.Repo)
			}
			return nil
		},
	}
	generate.Flags().BoolVar(&check, "check", false, "write nothing; list the stale blocks and fail if there are any")
	generate.Flags().BoolVar(&genJSON, "json", false, "print JSON")

	w.AddCommand(list, read, put, generate)
	return w
}

func printGenerate(out io.Writer, g server.WikiGenerateOut, check bool) {
	for _, s := range g.Stale {
		fmt.Fprintf(out, "stale: %s %s\n", s.Page, s.Block)
	}
	switch {
	case check && len(g.Stale) == 0:
		fmt.Fprintln(out, "no stale generated blocks")
	case !check && g.Commit == "":
		fmt.Fprintln(out, "nothing to regenerate")
	case !check:
		fmt.Fprintf(out, "regenerated %d files in %s\n", len(g.Changed), shortSHA(g.Commit))
	}
	if len(g.Problems) > 0 {
		fmt.Fprintf(out, "%d problems on the health page\n", len(g.Problems))
	}
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func without(xs []string, x string) []string {
	var out []string
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func repoQuery(repo string) url.Values {
	q := url.Values{}
	if repo != "" {
		q.Set("repo", repo)
	}
	return q
}

// wikiCaller is who a wiki command acts as: the run or actor worktree it
// runs in, else the owner, for this checkout's repository or --repo.
func wikiCaller(g *globals, repo string) (*client.Client, string, error) {
	if agentContext() {
		a, err := resolveAgent()
		if err != nil {
			return nil, "", err
		}
		if repo == "" {
			repo = a.defaultRepo()
		}
		return a.client(), repo, nil
	}
	inst, err := g.loadInstance()
	if err != nil {
		return nil, "", err
	}
	key, err := ownerKey(g)
	if err != nil {
		return nil, "", err
	}
	if repo == "" {
		top, err := runs.Git(".", nil, "rev-parse", "--show-toplevel")
		if err != nil {
			return nil, "", errors.New("outside a checkout, name the repository with --repo")
		}
		full, ok := repoOnInstance(top, inst)
		if !ok {
			return nil, "", fmt.Errorf("%s is not hosted on instance %s; name the repository with --repo", top, inst.Name)
		}
		_, repo, _ = strings.Cut(full, "/")
	}
	return client.New(inst.ServerURL(), key), repo, nil
}

// agentContext reports whether the command runs for an agent: in a run
// ($BOTTOWN_RUN_FILE) or an actor worktree.
func agentContext() bool {
	if os.Getenv("BOTTOWN_RUN_FILE") != "" {
		return true
	}
	top, err := runs.Git(".", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	actor, _ := runs.Git(top, nil, "config", "--worktree", "--get", gitActorKey)
	return actor != ""
}
