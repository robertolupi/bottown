package cli

import (
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

func newGit(g *globals) *cobra.Command {
	gitCmd := &cobra.Command{Use: "git", Short: "Git maintenance for the main checkout (owner)"}
	var dryRun bool
	var pattern string
	gc := &cobra.Command{
		Use:   "gc-branches",
		Short: "Delete branches merged into the default branch, on the forge and in the fam",
		Long: `Delete the branches matching --pattern (default issue/*) whose tip is merged
into the instance remote's default branch: first on the forge, then locally.
Actor worktrees share the main checkout's refs, so one pass in main covers the
whole fam.

A branch is kept, and the reason printed, when it is checked out in any
worktree (a review follow-up resumes issue/<n>; worktrees are never switched),
has a live run, or is the head of an open pull request. Unmerged branches are
never touched. Run it in the main checkout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return gcBranches(cmd, g, pattern, dryRun)
		},
	}
	gc.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be deleted, delete nothing")
	gc.Flags().StringVar(&pattern, "pattern", "issue/*", "branches to consider, as a git ref pattern below refs/heads/")
	gitCmd.AddCommand(gc)
	return gitCmd
}

// branchGC is what gc-branches deletes and keeps. Remote and Local hold
// branch names (without refs/heads/); SHAs maps a remote branch to the tip
// that was found merged, so the delete is leased on it.
type branchGC struct {
	Remote, Local []string
	SHAs          map[string]string
	Kept          []keptBranch
}

type keptBranch struct{ Branch, Reason string }

// branchGuards are the reasons to keep a merged branch, by branch name.
type branchGuards struct {
	CheckedOut map[string]string // branch -> worktree path
	Runs       map[string]string // branch -> live run ID
	PRs        map[string]int64  // branch -> open PR number
}

func (gd branchGuards) reason(b string) string {
	switch {
	case gd.CheckedOut[b] != "":
		return "checked out in " + gd.CheckedOut[b]
	case gd.Runs[b] != "":
		return "live run " + gd.Runs[b]
	case gd.PRs[b] != 0:
		return fmt.Sprintf("open pull request !%d", gd.PRs[b])
	}
	return ""
}

// planBranchGC decides from merged remote branches (name -> tip) and merged
// local branches. base, the default branch, is never deleted.
func planBranchGC(base string, remote map[string]string, local []string, gd branchGuards) branchGC {
	p := branchGC{SHAs: map[string]string{}}
	kept := map[string]bool{}
	keep := func(b string) bool {
		if b == base {
			return true
		}
		r := gd.reason(b)
		if r != "" && !kept[b] {
			kept[b] = true
			p.Kept = append(p.Kept, keptBranch{b, r})
		}
		return r != ""
	}
	names := make([]string, 0, len(remote))
	for b := range remote {
		names = append(names, b)
	}
	slices.Sort(names)
	for _, b := range names {
		if !keep(b) {
			p.Remote = append(p.Remote, b)
			p.SHAs[b] = remote[b]
		}
	}
	local = slices.Sorted(slices.Values(local))
	for _, b := range local {
		if !keep(b) {
			p.Local = append(p.Local, b)
		}
	}
	return p
}

// mergedBranches lists the branches under prefix ("refs/remotes/<remote>/" or
// "refs/heads/") matching pattern whose tips are reachable from into, with
// their tips.
func mergedBranches(top, into, prefix, pattern string) (map[string]string, error) {
	out, err := runs.Git(top, nil, "for-each-ref", "--merged", into, "--format=%(objectname) %(refname)", prefix+pattern)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		sha, ref, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		b := strings.TrimPrefix(ref, prefix)
		if b == "HEAD" {
			continue // refs/remotes/<remote>/HEAD
		}
		m[b] = sha
	}
	return m, nil
}

// checkedOutBranches maps each branch checked out in a worktree of top
// (main itself, actor and run worktrees) to that worktree's path.
func checkedOutBranches(top string) (map[string]string, error) {
	out, err := runs.Git(top, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, block := range strings.Split(out, "\n\n") {
		var p, b string
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "worktree "); ok {
				p = v
			} else if v, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
				b = v
			}
		}
		if p != "" && b != "" {
			m[b] = p
		}
	}
	return m, nil
}

// liveRunBranches maps the branch of each live run in org/repo to its ID.
func liveRunBranches(all []secrets.Run, org, repo string, now time.Time) map[string]string {
	m := map[string]string{}
	for _, r := range all {
		if r.Org != org || r.Repo != repo || r.Ended != nil || !now.Before(r.Expires) {
			continue
		}
		b := r.Branch
		if b == "" && r.Kind == "implement" && r.Issue > 0 {
			b = fmt.Sprintf("issue/%d", r.Issue)
		}
		if b != "" {
			m[b] = r.ID
		}
	}
	return m
}

func gcBranches(cmd *cobra.Command, g *globals, pattern string, dryRun bool) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	if pattern == "" || strings.HasPrefix(pattern, "refs/") {
		return errors.New("--pattern is a branch pattern below refs/heads/, such as issue/*")
	}
	inst, err := g.loadInstance()
	if err != nil {
		return err
	}
	top, err := mainCheckout()
	if err != nil {
		return err
	}
	full, ok := repoOnInstance(top, inst)
	if !ok {
		return fmt.Errorf("this checkout is not hosted on instance %s", inst.Name)
	}
	org, repo, _ := strings.Cut(full, "/")
	remote := inst.Remote()
	if _, err := runs.GitCtx(ctx, top, nil, "fetch", "-q", "--prune", remote); err != nil {
		return err
	}
	base, err := runs.RemoteDefaultBranch(ctx, top, remote)
	if err != nil {
		return err
	}
	into := "refs/remotes/" + remote + "/" + base
	merged, err := mergedBranches(top, into, "refs/remotes/"+remote+"/", pattern)
	if err != nil {
		return err
	}
	localMerged, err := mergedBranches(top, into, "refs/heads/", pattern)
	if err != nil {
		return err
	}
	local := make([]string, 0, len(localMerged))
	for b := range localMerged {
		local = append(local, b)
	}

	gd := branchGuards{PRs: map[string]int64{}}
	if gd.CheckedOut, err = checkedOutBranches(top); err != nil {
		return err
	}
	key, err := ownerKey(g)
	if err != nil {
		return err
	}
	var all []secrets.Run
	if err := callServer(ctx, inst, key, "GET", "/v1/runs", nil, &all); err != nil {
		return err
	}
	gd.Runs = liveRunBranches(all, org, repo, time.Now())
	for page := 1; ; page++ {
		var prs []struct {
			Number int64
			Head   struct{ Ref string }
		}
		if err := callServer(ctx, inst, key, "GET", fmt.Sprintf("/forgejo/api/v1/repos/%s/pulls?state=open&limit=50&page=%d", full, page), nil, &prs); err != nil {
			return err
		}
		for _, pr := range prs {
			gd.PRs[pr.Head.Ref] = pr.Number
		}
		if len(prs) < 50 {
			break
		}
	}

	p := planBranchGC(base, merged, local, gd)
	if !dryRun {
		if len(p.Remote) > 0 {
			// Leased on the tip found merged: a branch that moved since
			// the fetch is left alone.
			args := []string{"push", "-q", remote}
			for _, b := range p.Remote {
				args = append(args, "--force-with-lease=refs/heads/"+b+":"+p.SHAs[b])
			}
			for _, b := range p.Remote {
				args = append(args, ":refs/heads/"+b)
			}
			if _, err := runs.GitCtx(ctx, top, nil, args...); err != nil {
				return err
			}
		}
		// -D, not -d: -d compares with HEAD or a (just deleted) upstream,
		// not with the forge's default branch, which mergedBranches did.
		if len(p.Local) > 0 {
			if _, err := runs.Git(top, nil, append([]string{"branch", "-q", "-D"}, p.Local...)...); err != nil {
				return err
			}
		}
	}
	printBranchGC(out, lipgloss.NewRenderer(out), remote, base, p, dryRun)
	return nil
}

func printBranchGC(out io.Writer, r *lipgloss.Renderer, remote, base string, p branchGC, dryRun bool) {
	st := newStyles(r)
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}
	for _, b := range p.Remote {
		fmt.Fprintf(out, "%s %s\n", verb, st.Bold.Render(path.Join(remote, b)))
	}
	for _, b := range p.Local {
		fmt.Fprintf(out, "%s %s %s\n", verb, st.Bold.Render(b), st.Dim.Render("(local)"))
	}
	for _, k := range p.Kept {
		fmt.Fprintf(out, "kept %s: %s\n", st.Bold.Render(k.Branch), st.Warning.Render(k.Reason))
	}
	fmt.Fprintf(out, "%s %d remote and %d local branches merged into %s/%s; kept %d\n",
		verb, len(p.Remote), len(p.Local), remote, base, len(p.Kept))
}
