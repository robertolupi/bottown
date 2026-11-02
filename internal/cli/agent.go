package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// The agent CLI: commands agents run in their worktree to work with the
// forge. They hold no forge token; the server enforces the run's role,
// repository and commit pin.

// agentCtx is who an agent command acts as.
type agentCtx struct {
	Actor     string
	ServerURL string
	Key       string
	RunID     string // empty outside a run
	Worktree  string // actor worktree top, if any
	KeyDir    string // <gitdir>/bottown of the actor worktree, if any
	Remote    string // the bottown remote name in this worktree
	AttachDir string // the run's attachments directory; empty outside a run
}

// resolveAgent finds the run or actor worktree a command acts for:
// $BOTTOWN_RUN_FILE (set by `bottown run implement/verify` launches), else
// the actor worktree containing the working directory.
func resolveAgent() (*agentCtx, error) {
	if f := os.Getenv("BOTTOWN_RUN_FILE"); f != "" {
		rec, err := runs.LoadFile(f)
		if err != nil {
			return nil, err
		}
		if rec.Key == "" {
			return nil, fmt.Errorf("run %s has ended", rec.ID)
		}
		return &agentCtx{Actor: rec.Actor, ServerURL: rec.ServerURL, Key: rec.Key, RunID: rec.ID, Remote: "origin", AttachDir: rec.Attachments}, nil
	}
	top, err := git(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, errors.New("not inside a git checkout")
	}
	actor, _ := git(top, "config", "--worktree", "--get", gitActorKey)
	if actor == "" {
		return nil, errors.New("not an actor worktree (run `bottown actor setup <actor>` in the main checkout), and no run in $BOTTOWN_RUN_FILE")
	}
	gitDir, err := git(top, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	a := &agentCtx{Actor: actor, Worktree: top, KeyDir: filepath.Join(gitDir, "bottown")}
	if rec, err := runs.LoadFile(filepath.Join(a.KeyDir, "run.json")); err == nil && rec.Key != "" {
		a.ServerURL, a.Key, a.RunID, a.AttachDir = rec.ServerURL, rec.Key, rec.ID, rec.Attachments
	} else if rec, err := runs.LoadFile(filepath.Join(a.KeyDir, "actor.json")); err == nil && rec.Key != "" {
		a.ServerURL, a.Key = rec.ServerURL, rec.Key
	} else {
		return nil, fmt.Errorf("actor worktree %s has no key; run `bottown actor setup %s` in the main checkout", top, actor)
	}
	inst, _ := git(top, "config", "--get", gitInstanceKey)
	if inst == "" {
		inst = config.DefaultInstance
	}
	a.Remote = config.Instance{Name: inst}.Remote()
	return a, nil
}

// call makes a JSON request to the bottown server with the agent's key.
func (a *agentCtx) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.ServerURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.Key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("bottown server unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && (e.Error != "" || e.Message != "") {
			return errors.New(e.Error + e.Message)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// readBody returns --body, or the contents of --body-file ("-" is stdin).
func readBody(cmd *cobra.Command, body, file string) (string, error) {
	switch {
	case body != "" && file != "":
		return "", errors.New("--body and --body-file are mutually exclusive")
	case file == "-":
		b, err := io.ReadAll(cmd.InOrStdin())
		return string(b), err
	case file != "":
		b, err := os.ReadFile(file)
		return string(b), err
	}
	return body, nil
}

func printJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func newWhoami() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show which actor and run this worktree acts as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			var who struct {
				Actor    string          `json:"actor"`
				Instance string          `json:"instance"`
				RunRaw   json.RawMessage `json:"run"`
			}
			if err := a.call("GET", "/v1/whoami", nil, &who); err != nil {
				return err
			}
			var last *runs.Record
			var head *headInfo
			if a.Worktree != "" {
				if a.RunID == "" {
					if rec, err := runs.LoadFile(filepath.Join(a.KeyDir, "run.json")); err == nil && rec.ID != "" {
						last = &rec
					}
				}
				head = worktreeHead(cmd.Context(), a.Worktree, a.Remote)
			}
			if asJSON {
				m := map[string]any{"actor": who.Actor, "instance": who.Instance, "run": who.RunRaw, "worktree": a.Worktree}
				if last != nil {
					m["last_run"] = map[string]any{"id": last.ID, "kind": last.Kind, "repo": last.Repo, "issue": last.Issue, "pr": last.PR, "status": last.Status, "ended": last.Ended}
				}
				if head != nil {
					m["head"] = head
				}
				return printJSON(cmd, m)
			}
			out := cmd.OutOrStdout()
			var run *whoamiRun
			if len(who.RunRaw) > 0 && string(who.RunRaw) != "null" {
				var wr whoamiRun
				if json.Unmarshal(who.RunRaw, &wr) == nil {
					run = &wr
				}
			}
			printWhoami(out, lipgloss.NewRenderer(out), who.Actor, who.Instance, run, last, head)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type whoamiRun struct {
	ID, Kind, Repo, Branch, SHA string
	Issue, PR                   int64
}

func printWhoami(out io.Writer, r *lipgloss.Renderer, actor, instance string, run *whoamiRun, last *runs.Record, head *headInfo) {
	st := newStyles(r)
	fmt.Fprintf(out, "actor %s on instance %s\n", st.Info.Bold(true).Render(actor), st.Bold.Render(instance))
	if run != nil {
		target := fmt.Sprintf("issue #%d", run.Issue)
		if run.PR > 0 {
			target = fmt.Sprintf("PR !%d at %s", run.PR, run.SHA[:min(10, len(run.SHA))])
		}
		fmt.Fprintf(out, "run %s: %s %s in %s\n",
			st.Info.Bold(true).Render(run.ID),
			st.Purple.Bold(true).Render(run.Kind),
			st.Bold.Render(target),
			st.Bold.Render(run.Repo),
		)
	} else {
		fmt.Fprintln(out, st.Dim.Render("no active run (read-only; start one with `bottown run start implement|verify N`)"))
		if last != nil {
			fmt.Fprintln(out, lastRunLine(*last))
		}
	}
	if head != nil {
		line := head.String()
		if head.Behind > 0 || head.Base == "" || !head.Fetched {
			line = st.Warning.Render(line)
		}
		fmt.Fprintln(out, line)
	}
}

// lastRunLine describes the run that last used this worktree.
func lastRunLine(r runs.Record) string {
	target := fmt.Sprintf("issue #%d", r.Issue)
	if r.PR > 0 {
		target = fmt.Sprintf("PR !%d", r.PR)
	}
	s := fmt.Sprintf("last run %s: %s %s in %s, %s", r.ID, r.Kind, target, r.Repo, r.Status)
	if r.Ended != nil {
		s += ", ended " + r.Ended.Local().Format("2006-01-02 15:04")
	}
	return s
}

// headInfo is where a worktree's HEAD is relative to the bottown remote's
// default branch.
type headInfo struct {
	SHA     string `json:"sha"`
	Branch  string `json:"branch,omitempty"` // empty when detached
	Base    string `json:"base,omitempty"`   // e.g. bottown/main; empty when unknown
	Ahead   int    `json:"ahead"`
	Behind  int    `json:"behind"`
	Fetched bool   `json:"fetched"` // false: compared with the last fetch
}

// worktreeHead fetches the remote (briefly, so a merge since the last run
// shows) and compares HEAD with its default branch. It returns nil when
// HEAD cannot be read.
func worktreeHead(ctx context.Context, top, remote string) *headInfo {
	sha, err := git(top, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil
	}
	h := &headInfo{SHA: sha}
	h.Branch, _ = git(top, "symbolic-ref", "-q", "--short", "HEAD")
	fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = runs.GitCtx(fctx, top, nil, "fetch", "-q", remote)
	h.Fetched = err == nil
	branch, err := remoteDefaultBranch(top, remote)
	if err != nil {
		return h
	}
	base := remote + "/" + branch
	counts, err := git(top, "rev-list", "--left-right", "--count", "HEAD..."+base)
	if err != nil {
		return h
	}
	if _, err := fmt.Sscanf(counts, "%d %d", &h.Ahead, &h.Behind); err != nil {
		return h
	}
	h.Base = base
	return h
}

func (h headInfo) String() string {
	s := "HEAD " + h.SHA
	if h.Branch != "" {
		s += " on " + h.Branch
	} else {
		s += " (detached)"
	}
	if h.Base == "" {
		return s + ", default branch unknown"
	}
	switch {
	case h.Ahead == 0 && h.Behind == 0:
		s += ", at " + h.Base
	case h.Ahead == 0:
		s += fmt.Sprintf(", %s behind %s", commits(h.Behind), h.Base)
	case h.Behind == 0:
		s += fmt.Sprintf(", %s ahead of %s", commits(h.Ahead), h.Base)
	default:
		s += fmt.Sprintf(", %s ahead of and %d behind %s", commits(h.Ahead), h.Behind, h.Base)
	}
	if !h.Fetched {
		s += " (fetch failed; as of the last fetch)"
	}
	return s
}

func commits(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

func targetQuery(repo string, n int64, extra url.Values) string {
	q := url.Values{}
	if repo != "" {
		q.Set("repo", repo)
	}
	if n > 0 {
		q.Set("number", fmt.Sprint(n))
	}
	for k, v := range extra {
		q[k] = v
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func optNumber(args []string) (int64, error) {
	if len(args) == 0 {
		return 0, nil
	}
	return parseNumber(args[0])
}

// repoFromGitURL returns org and repository from a bottown git remote URL,
// e.g. http://127.0.0.1:3031/git/bottown/demo.git.
func repoFromGitURL(u string) (org, repo string, ok bool) {
	_, rest, found := strings.Cut(u, "/git/")
	if !found {
		return "", "", false
	}
	org, repo, found = strings.Cut(strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git"), "/")
	if !found || org == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return org, repo, true
}

// defaultRepo is the repository an agent command without --repo targets
// outside a run: the one the worktree's bottown remote points at. In a run
// it is empty, and the server uses the run's.
func (a *agentCtx) defaultRepo() string {
	if a.RunID != "" || a.Worktree == "" {
		return ""
	}
	u, err := git(a.Worktree, "remote", "get-url", a.Remote)
	if err != nil {
		return ""
	}
	_, repo, _ := repoFromGitURL(u)
	return repo
}

func newIssue() *cobra.Command {
	issue := &cobra.Command{Use: "issue", Short: "Read and file issues (agent CLI)"}
	var repo, download string
	var asJSON bool
	show := &cobra.Command{
		Use:   "show [N] [--download DIR]",
		Short: "Show an issue, its comments and attachments (default: the run's issue)",
		Long: `Show an issue, its comments and its attachments: the files on its
description and comments. In a run they are downloaded into the run's
attachments directory, which is removed when the run ends, and listed with
their local paths; outside a run, --download DIR saves them there.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			n, err := optNumber(args)
			if err != nil {
				return err
			}
			r := repo
			if r == "" {
				r = a.defaultRepo()
			}
			var is server.IssueOut
			if err := a.call("GET", "/v1/forge/issue"+targetQuery(r, n, nil), nil, &is); err != nil {
				return err
			}
			dir := download
			if dir == "" {
				dir = a.AttachDir
			}
			var atts []runs.Attachment
			if dir != "" {
				atts = a.fetchAttachments(cmd.Context(), dir, r, is)
			} else {
				atts = attachmentList(is)
				for i := range atts {
					atts[i].Error = "not downloaded outside a run (use --download DIR)"
				}
			}
			if asJSON {
				return printJSON(cmd, issueShowJSON{IssueOut: is, Attachments: atts})
			}
			out := cmd.OutOrStdout()
			printIssueShow(out, lipgloss.NewRenderer(out), is, atts)
			return nil
		},
	}
	show.Flags().StringVar(&repo, "repo", "", "repository (default: the run's, else the one this worktree's bottown remote points at)")
	show.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	show.Flags().StringVar(&download, "download", "", "download the attachments into DIR (in a run, the default is the run's attachments directory)")

	var createRepo, title, body, bodyFile string
	var createJSON bool
	create := &cobra.Command{
		Use:   "create --title T [--body B | --body-file F|-]",
		Short: "File a new issue as yourself, in or outside a run (default repo: the run's, else this worktree's)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			b, err := readBody(cmd, body, bodyFile)
			if err != nil {
				return err
			}
			r := createRepo
			if r == "" {
				r = a.defaultRepo()
			}
			var out server.IssueCreateOut
			if err := a.call("POST", "/v1/forge/issue", server.IssueCreateIn{Repo: r, Title: title, Body: b}, &out); err != nil {
				return err
			}
			if createJSON {
				return printJSON(cmd, out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "filed #%d in %s: %s\n", out.Number, out.Repo, out.URL)
			return nil
		},
	}
	create.Flags().StringVar(&createRepo, "repo", "", "repository (default: the run's, else the one this worktree's bottown remote points at)")
	create.Flags().StringVar(&title, "title", "", "title (required)")
	create.Flags().StringVar(&body, "body", "", "description")
	create.Flags().StringVar(&bodyFile, "body-file", "", "read the description from a file, or - for stdin")
	create.Flags().BoolVar(&createJSON, "json", false, "print JSON")
	_ = create.MarkFlagRequired("title")

	var listRepo, listState string
	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List issues in a repository (default repo: the run's, else this worktree's)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			r := listRepo
			if r == "" {
				r = a.defaultRepo()
			}
			q := url.Values{}
			if r != "" {
				q.Set("repo", r)
			}
			if listState != "" {
				q.Set("state", listState)
			}
			var issues []server.IssueItem
			if err := a.call("GET", "/v1/forge/issues?"+q.Encode(), nil, &issues); err != nil {
				return err
			}
			if listJSON {
				return printJSON(cmd, issues)
			}
			out := cmd.OutOrStdout()
			printIssueList(out, lipgloss.NewRenderer(out), issues)
			return nil
		},
	}
	list.Flags().StringVar(&listRepo, "repo", "", "repository (default: the run's, else the one this worktree's bottown remote points at)")
	list.Flags().StringVar(&listState, "state", "open", "state filter: open, closed, or all")
	list.Flags().BoolVar(&listJSON, "json", false, "print JSON")
	issue.AddCommand(show, create, list)
	return issue
}

func newPR() *cobra.Command {
	pr := &cobra.Command{Use: "pr", Short: "Read and open pull requests (agent CLI)"}
	var repo string
	var asJSON, diff, stat, full bool
	show := &cobra.Command{
		Use:   "show [N] [--diff [--full] | --stat]",
		Short: "Show a pull request: head, reviews, and with --diff a file summary and the diff (default: the run's PR)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			n, err := optNumber(args)
			if err != nil {
				return err
			}
			r := repo
			if r == "" {
				r = a.defaultRepo()
			}
			extra := url.Values{}
			if diff || stat {
				extra.Set("diff", "1")
			}
			var p server.PROut
			if err := a.call("GET", "/v1/forge/pr"+targetQuery(r, n, extra), nil, &p); err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd, p)
			}
			out := cmd.OutOrStdout()
			printPRShow(out, lipgloss.NewRenderer(out), p, diff, stat, full)
			return nil
		},
	}
	show.Flags().StringVar(&repo, "repo", "", "repository (default: the run's, else the one this worktree's bottown remote points at)")
	show.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	show.Flags().BoolVar(&diff, "diff", false, "include a file summary and the diff, with lock files such as go.sum collapsed")
	show.Flags().BoolVar(&stat, "stat", false, "include only the file summary")
	show.Flags().BoolVar(&full, "full", false, "with --diff, do not collapse lock files")
	show.MarkFlagsMutuallyExclusive("diff", "stat")

	var title, body, bodyFile string
	var createJSON bool
	create := &cobra.Command{
		Use:   "create --title T [--body B | --body-file F|-]",
		Short: "Open a pull request from this implement run's branch (push it first)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			b, err := readBody(cmd, body, bodyFile)
			if err != nil {
				return err
			}
			var out server.PRCreateOut
			if err := a.call("POST", "/v1/forge/pr", server.PRCreateIn{Title: title, Body: b}, &out); err != nil {
				return err
			}
			if createJSON {
				return printJSON(cmd, out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "opened !%d (%s -> %s): %s\n", out.Number, out.Head, out.Base, out.URL)
			return nil
		},
	}
	create.Flags().StringVar(&title, "title", "", "title (required)")
	create.Flags().StringVar(&body, "body", "", "description")
	create.Flags().StringVar(&bodyFile, "body-file", "", "read the description from a file, or - for stdin")
	create.Flags().BoolVar(&createJSON, "json", false, "print JSON")
	_ = create.MarkFlagRequired("title")

	var listRepo, listState string
	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List pull requests in a repository (default repo: the run's, else this worktree's)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			r := listRepo
			if r == "" {
				r = a.defaultRepo()
			}
			q := url.Values{}
			if r != "" {
				q.Set("repo", r)
			}
			if listState != "" {
				q.Set("state", listState)
			}
			var prs []server.PRItem
			if err := a.call("GET", "/v1/forge/prs?"+q.Encode(), nil, &prs); err != nil {
				return err
			}
			if listJSON {
				return printJSON(cmd, prs)
			}
			out := cmd.OutOrStdout()
			printPRList(out, lipgloss.NewRenderer(out), prs)
			return nil
		},
	}
	list.Flags().StringVar(&listRepo, "repo", "", "repository (default: the run's, else the one this worktree's bottown remote points at)")
	list.Flags().StringVar(&listState, "state", "open", "state filter: open, closed, or all")
	list.Flags().BoolVar(&listJSON, "json", false, "print JSON")
	pr.AddCommand(show, create, list)
	return pr
}

func newComment() *cobra.Command {
	var repo, body, bodyFile string
	cmd := &cobra.Command{
		Use:   "comment [N] (--body B | --body-file F|-)",
		Short: "Comment on an issue or PR (default: the run's issue, or PR for verify runs)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			n, err := optNumber(args)
			if err != nil {
				return err
			}
			b, err := readBody(cmd, body, bodyFile)
			if err != nil {
				return err
			}
			r := repo
			if r == "" {
				r = a.defaultRepo()
			}
			var out server.CommentOut
			if err := a.call("POST", "/v1/forge/comment", server.CommentIn{Target: server.Target{Repo: r, Number: n}, Body: b}, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "commented (id %d) as %s\n", out.ID, out.Author)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository (default: the run's, else the one this worktree's bottown remote points at)")
	cmd.Flags().StringVar(&body, "body", "", "comment text")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "read the comment from a file, or - for stdin")
	return cmd
}

func newReview() *cobra.Command {
	review := &cobra.Command{Use: "review", Short: "Submit reviews (agent CLI, verify runs)"}
	var event, commit, body, bodyFile string
	submit := &cobra.Command{
		Use:   "submit --event APPROVED|REQUEST_CHANGES|COMMENT --commit SHA (--body B | --body-file F|-)",
		Short: "Submit this verify run's verdict, pinned to the commit you verified",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			b, err := readBody(cmd, body, bodyFile)
			if err != nil {
				return err
			}
			var out server.ReviewOut
			if err := a.call("POST", "/v1/forge/review", server.ReviewIn{Event: event, Body: b, CommitID: commit}, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "review %s by %s at %s (official %v)\n", out.State, out.Reviewer, out.CommitID, out.Official)
			return nil
		},
	}
	submit.Flags().StringVar(&event, "event", "", "APPROVED, REQUEST_CHANGES or COMMENT (required)")
	submit.Flags().StringVar(&commit, "commit", "", "the SHA you verified (required)")
	submit.Flags().StringVar(&body, "body", "", "the verdict and its reasons")
	submit.Flags().StringVar(&bodyFile, "body-file", "", "read the verdict from a file, or - for stdin")
	_ = submit.MarkFlagRequired("event")
	_ = submit.MarkFlagRequired("commit")
	review.AddCommand(submit)
	return review
}

func newAPI() *cobra.Command {
	var data string
	cmd := &cobra.Command{
		Use:   "api METHOD PATH [--data JSON]",
		Short: "Call any Forgejo API endpoint below /api/v1 as yourself, under bottown's policy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := resolveAgent()
			if err != nil {
				return err
			}
			if !strings.HasPrefix(args[1], "/") {
				return errors.New("PATH must start with /, e.g. /repos/bottown/x/issues")
			}
			var body io.Reader
			if data != "" {
				if data == "-" {
					b, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return err
					}
					data = string(b)
				}
				if !json.Valid([]byte(data)) {
					return errors.New("--data must be JSON")
				}
				body = strings.NewReader(data)
			}
			req, err := http.NewRequest(strings.ToUpper(args[0]), a.ServerURL+"/forgejo/api/v1"+args[1], body)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+a.Key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			var buf bytes.Buffer
			if json.Indent(&buf, raw, "", "  ") == nil {
				raw = buf.Bytes()
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n", bytes.TrimSpace(raw))
			if resp.StatusCode >= 300 {
				return fmt.Errorf("%s", resp.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "JSON request body, or - for stdin")
	return cmd
}

func printIssueShow(out io.Writer, r *lipgloss.Renderer, is server.IssueOut, atts []runs.Attachment) {
	st := newStyles(r)
	fmt.Fprintf(out, "%s %s [%s] by %s",
		st.Info.Bold(true).Render(fmt.Sprintf("#%d", is.Number)),
		st.Bold.Render(is.Title),
		st.issueState(is.State),
		st.Bold.Render(is.Author),
	)
	if len(is.Labels) > 0 {
		fmt.Fprintf(out, " (%s)", strings.Join(is.Labels, ", "))
	}
	fmt.Fprintf(out, "\n\n%s\n", strings.TrimSpace(is.Body))
	for _, c := range is.Comments {
		fmt.Fprintf(out, "\n--- %s, %s\n%s\n",
			st.Bold.Render(c.Author),
			st.Dim.Render(c.Created.Local().Format(time.DateTime)),
			strings.TrimSpace(c.Body),
		)
	}
	if len(atts) > 0 || is.AttachmentsError != "" {
		fmt.Fprintf(out, "\n--- %s\n", st.Bold.Render(fmt.Sprintf("attachments (%d)", len(atts))))
		for _, at := range atts {
			line := "- " + at.String()
			if at.Path == "" {
				line = st.Warning.Render(line)
			}
			fmt.Fprintln(out, line)
		}
		if is.AttachmentsError != "" {
			fmt.Fprintln(out, st.Warning.Render("- the attachments could not be listed: "+is.AttachmentsError))
		}
	}
}

func printIssueList(out io.Writer, r *lipgloss.Renderer, issues []server.IssueItem) {
	if len(issues) == 0 {
		fmt.Fprintln(out, "no issues found")
		return
	}
	st := newStyles(r)
	for _, is := range issues {
		labels := ""
		if len(is.Labels) > 0 {
			labels = " (" + strings.Join(is.Labels, ", ") + ")"
		}
		numStr := fmt.Sprintf("#%-4d", is.Number)
		statePad := 8 - len(is.State)
		if statePad < 0 {
			statePad = 0
		}
		authorPad := 16 - len(is.Author)
		if authorPad < 0 {
			authorPad = 0
		}
		fmt.Fprintf(out, "%s %s%s %s%s %s%s\n",
			st.Info.Bold(true).Render(numStr),
			st.issueState(is.State), strings.Repeat(" ", statePad),
			st.Bold.Render(is.Author), strings.Repeat(" ", authorPad),
			is.Title, labels,
		)
	}
}

func printPRShow(out io.Writer, r *lipgloss.Renderer, p server.PROut, diff, stat, full bool) {
	st := newStyles(r)
	state := p.State
	if p.Merged {
		state = "merged"
	}
	fmt.Fprintf(out, "%s %s [%s] by %s\n%s -> %s at %s, mergeable %v\n\n%s\n",
		st.Info.Bold(true).Render(fmt.Sprintf("!%d", p.Number)),
		st.Bold.Render(p.Title),
		st.prState(state),
		st.Bold.Render(p.Author),
		st.Bold.Render(p.HeadBranch),
		st.Bold.Render(p.BaseBranch),
		st.Dim.Render(p.HeadSHA),
		p.Mergeable,
		strings.TrimSpace(p.Body),
	)
	for _, rev := range p.Reviews {
		flags := ""
		if rev.Stale {
			flags += " " + st.Warning.Render("stale")
		}
		if rev.Dismissed {
			flags += " " + st.Warning.Render("dismissed")
		}
		commit := rev.CommitID[:min(10, len(rev.CommitID))]
		fmt.Fprintf(out, "\n--- review %s by %s at %s%s\n%s\n",
			st.reviewVerdict(rev.State),
			st.Bold.Render(rev.Reviewer),
			st.Dim.Render(commit),
			flags,
			strings.TrimSpace(rev.Body),
		)
	}
	if diff || stat {
		files := parseDiff(p.Diff)
		if full {
			for i := range files {
				files[i].Collapsed = false
			}
		}
		fmt.Fprintln(out)
		writeDiffStat(out, files)
		if p.DiffTruncated {
			fmt.Fprintln(out, " [diff truncated: the summary covers only what was received]")
		}
		if diff {
			fmt.Fprintln(out)
			writeDiff(out, files)
			if p.DiffTruncated {
				fmt.Fprintln(out, "[diff truncated]")
			}
		}
	}
}

func printPRList(out io.Writer, r *lipgloss.Renderer, prs []server.PRItem) {
	if len(prs) == 0 {
		fmt.Fprintln(out, "no pull requests found")
		return
	}
	st := newStyles(r)
	for _, p := range prs {
		sha := p.HeadSHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		numStr := fmt.Sprintf("!%-4d", p.Number)
		statePad := 8 - len(p.State)
		if statePad < 0 {
			statePad = 0
		}
		shaPad := 10 - len(sha)
		if shaPad < 0 {
			shaPad = 0
		}
		authorPad := 16 - len(p.Author)
		if authorPad < 0 {
			authorPad = 0
		}
		revPad := 18 - len(p.ReviewState)
		if revPad < 0 {
			revPad = 0
		}
		fmt.Fprintf(out, "%s %s%s %s%s %s%s %s%s %s\n",
			st.Info.Bold(true).Render(numStr),
			st.prState(p.State), strings.Repeat(" ", statePad),
			st.Dim.Render(sha), strings.Repeat(" ", shaPad),
			st.Bold.Render(p.Author), strings.Repeat(" ", authorPad),
			st.reviewVerdict(p.ReviewState), strings.Repeat(" ", revPad),
			p.Title,
		)
	}
}
