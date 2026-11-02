package milestone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/robertolupi/bottown/internal/config"
)

// ErrNotFound is what a Forge returns for an issue or file that is not there.
var ErrNotFound = errors.New("not found")

// Forge is what Apply needs of one repository on the forge, acting as the
// owner. The CLI implements it over the server's Forgejo API proxy; tests
// use a fake.
type Forge interface {
	// DefaultBranch is the repository's default branch and its head commit.
	DefaultBranch(ctx context.Context) (name, sha string, err error)
	// LastCommit is the newest commit at or before ref that changed path,
	// or "" if there is none.
	LastCommit(ctx context.Context, ref, path string) (string, error)
	// ReadFile reads a file at a commit: ErrNotFound if it is not a file.
	ReadFile(ctx context.Context, ref, path string) ([]byte, error)
	// Issue fetches one issue or pull request: ErrNotFound if neither.
	Issue(ctx context.Context, n int64) (*ForgeIssue, error)
	// Issues lists every issue, open and closed, and no pull requests.
	Issues(ctx context.Context) ([]*ForgeIssue, error)
	CreateIssue(ctx context.Context, title, body string, milestone int64) (int64, error)
	EditIssue(ctx context.Context, n int64, title, body string) error
	// BlockedBy lists the issues n depends on (Forgejo's dependencies).
	BlockedBy(ctx context.Context, n int64) ([]int64, error)
	AddBlockedBy(ctx context.Context, n, dep int64) error
	RemoveBlockedBy(ctx context.Context, n, dep int64) error
}

// ForgeIssue is the part of a Forgejo issue Apply reads.
type ForgeIssue struct {
	Number    int64
	Title     string
	Body      string
	Closed    bool
	Poster    string
	PR        bool
	Milestone *ForgeMilestone // nil if none
}

// ForgeMilestone is an issue's Forgejo milestone.
type ForgeMilestone struct {
	ID     int64
	Title  string
	Closed bool
}

func (is *ForgeIssue) state() string {
	if is.Closed {
		return "closed"
	}
	return "open"
}

// ApplyOptions says what Apply applies and how.
type ApplyOptions struct {
	Org, Repo string // for links in issue bodies
	Owner     string // the owner's login: only their issues' markers count
	Slug      string
	DryRun    bool // preflight and print the changes, writing nothing
	Force     bool // overwrite open issues edited in Forgejo since the last apply
	Out       io.Writer
}

// A Report is what Apply found and did.
type Report struct {
	MainSHA   string
	PlanSHA   string // the commit that last changed the plan file
	Milestone ForgeMilestone
	Epic      int64 // 0 if a dry run would create it
	// Changes are the writes, planned before any is made.
	Changes []string
	// Flags are what the owner should look at: skipped and conflicting
	// updates, removed keys, ignored markers.
	Flags []string
	// Writes is how many writes were made.
	Writes int
}

// Apply reconciles the plan <milestones_dir>/<slug>.md on the default branch
// into the forge: child issues, the Implementation Epic and their blocked_by
// dependencies (docs/design/milestones.md §2.6). Every check runs before the
// first write, and every write is idempotent, so a failed apply resumes by
// running it again.
func Apply(ctx context.Context, f Forge, o ApplyOptions) (*Report, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Owner == "" {
		return nil, errors.New("apply needs the owner's login")
	}
	if !ValidSlug(o.Slug) {
		return nil, fmt.Errorf("plan slug %q must be lowercase words joined by -", o.Slug)
	}
	a := &applier{f: f, o: o, rep: &Report{}, byKey: map[string]*child{}, blockedBy: map[int64][]int64{}}
	if err := a.preflight(ctx); err != nil {
		return nil, err
	}
	a.print()
	if o.DryRun {
		fmt.Fprintln(o.Out, "dry run: nothing written")
		return a.rep, nil
	}
	err := a.execute(ctx)
	if err != nil {
		return a.rep, fmt.Errorf("%w (after %d write(s); run apply again to resume)", err, a.rep.Writes)
	}
	fmt.Fprintf(o.Out, "applied %s: %d write(s)\n", a.file, a.rep.Writes)
	return a.rep, nil
}

// A child is one of the plan's issues and its issue on the forge, if any.
type child struct {
	Issue
	cur     *ForgeIssue // nil until created
	content string      // the rendered body without the marker
	sha     string
	update  bool // PATCH cur to content
}

func (c *child) ref() string {
	if c.cur == nil {
		return fmt.Sprintf("(%s)", c.Key)
	}
	return fmt.Sprintf("#%d (%s)", c.cur.Number, c.Key)
}

// depChange is the blocked_by edges of one issue (a node) to add and remove.
type depChange struct {
	node        node
	add, remove []node
}

type applier struct {
	f   Forge
	o   ApplyOptions
	rep *Report

	branch, file string
	plan         *Plan
	kickoff      *ForgeIssue

	children []*child // in file order
	byKey    map[string]*child
	removed  []*ForgeIssue // issues with a marker for a key no longer in the plan
	epic     *ForgeIssue   // nil until created
	epicBody string        // the rendered epic body, when it changes
	epicNew  bool          // the epic is to be created or updated

	blockedBy map[int64][]int64 // the forge's dependencies, as fetched
	deps      []depChange
}

func (a *applier) preflight(ctx context.Context) error {
	f, rep := a.f, a.rep
	var err error
	if a.branch, rep.MainSHA, err = f.DefaultBranch(ctx); err != nil {
		return fmt.Errorf("resolving the default branch: %w", err)
	}
	settings := config.DefaultRepoSettings()
	switch b, err := f.ReadFile(ctx, rep.MainSHA, config.RepoSettingsFile); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return fmt.Errorf("reading %s: %w", config.RepoSettingsFile, err)
	default:
		if settings, err = config.ParseRepoSettings(b); err != nil {
			return fmt.Errorf("%s on %s: %w", config.RepoSettingsFile, a.branch, err)
		}
	}
	a.file = settings.MilestonesDir + "/" + a.o.Slug + ".md"
	src, err := f.ReadFile(ctx, rep.MainSHA, a.file)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%s is not on %s at %s: merge the plan before applying it", a.file, a.branch, short(rep.MainSHA))
	} else if err != nil {
		return fmt.Errorf("reading %s: %w", a.file, err)
	}
	if a.plan, err = Check(a.file, src); err != nil {
		return fmt.Errorf("%s on %s does not check:\n%w", a.file, a.branch, err)
	}
	if rep.PlanSHA, err = f.LastCommit(ctx, rep.MainSHA, a.file); err != nil {
		return fmt.Errorf("finding the commit of %s: %w", a.file, err)
	} else if rep.PlanSHA == "" {
		rep.PlanSHA = rep.MainSHA
	}

	// The kickoff issue gives the milestone.
	k := a.plan.Kickoff
	switch a.kickoff, err = f.Issue(ctx, k); {
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("kickoff #%d does not exist", k)
	case err != nil:
		return fmt.Errorf("fetching kickoff #%d: %w", k, err)
	case a.kickoff.PR:
		return fmt.Errorf("kickoff #%d is a pull request, not an issue", k)
	case a.kickoff.Milestone == nil:
		return fmt.Errorf("kickoff #%d has no milestone: put it in the milestone the plan is for", k)
	case a.kickoff.Milestone.Closed:
		return fmt.Errorf("kickoff #%d's milestone %q is closed", k, a.kickoff.Milestone.Title)
	}
	rep.Milestone = *a.kickoff.Milestone
	if d := a.plan.Design; d != "" {
		if _, err := f.ReadFile(ctx, rep.MainSHA, d); errors.Is(err, ErrNotFound) {
			return fmt.Errorf("design %q is not a file on %s at %s", d, a.branch, short(rep.MainSHA))
		} else if err != nil {
			return fmt.Errorf("reading design %q: %w", d, err)
		}
	}

	if err := a.discover(ctx); err != nil {
		return err
	}
	if err := a.externals(ctx); err != nil {
		return err
	}
	a.planChildren()
	a.planEpic()
	return a.planDeps(ctx)
}

// discover finds the plan's issues on the forge by their markers.
func (a *applier) discover(ctx context.Context) error {
	issues, err := a.f.Issues(ctx)
	if err != nil {
		return fmt.Errorf("listing issues: %w", err)
	}
	for i := range a.plan.Issues {
		c := &child{Issue: a.plan.Issues[i]}
		a.children = append(a.children, c)
		a.byKey[c.Key] = c
	}
	found := map[string]*ForgeIssue{} // key, or "" for the epic → issue
	var removed []string
	for _, is := range issues {
		if is.PR {
			continue
		}
		m, _, ok := ParseMarker(is.Body)
		if !ok || m.Plan != a.o.Slug {
			continue
		}
		if !Trusted(is.Poster, a.o.Owner) {
			a.flag("#%d: ignored its marker for plan %q: the issue is by %s, not the owner", is.Number, m.Plan, is.Poster)
			continue
		}
		what := "key " + strconv.Quote(m.Key)
		if m.Epic {
			what = "the epic"
		}
		if other, dup := found[m.Key]; dup {
			lo, hi := min(other.Number, is.Number), max(other.Number, is.Number)
			return fmt.Errorf("#%d and #%d both carry the marker for %s of plan %q: fix the markers by hand", lo, hi, what, m.Plan)
		}
		found[m.Key] = is
		if is.Milestone == nil || is.Milestone.ID != a.rep.Milestone.ID {
			in := "no milestone"
			if is.Milestone != nil {
				in = fmt.Sprintf("milestone %q", is.Milestone.Title)
			}
			return fmt.Errorf("#%d (%s of plan %q) is in %s, not kickoff #%d's milestone %q",
				is.Number, what, m.Plan, in, a.plan.Kickoff, a.rep.Milestone.Title)
		}
		switch c := a.byKey[m.Key]; {
		case m.Epic:
			a.epic = is
			a.rep.Epic = is.Number
		case c != nil:
			c.cur = is
		default:
			removed = append(removed, m.Key)
		}
	}
	// Removed keys in issue order, for stable output.
	for _, is := range issues {
		if m, _, ok := ParseMarker(is.Body); ok && m.Plan == a.o.Slug && !m.Epic && slices.Contains(removed, m.Key) && found[m.Key] == is {
			a.removed = append(a.removed, is)
			a.flag("#%d (%s): removed from the plan; left %s for the owner to close or re-triage", is.Number, m.Key, is.state())
		}
	}
	return nil
}

// externals checks the plan's "#<N>" dependencies.
func (a *applier) externals(ctx context.Context) error {
	for _, c := range a.children {
		for _, d := range c.Depends {
			n, ok := issueRef(d)
			if !ok {
				continue
			}
			if a.epic != nil && n == a.epic.Number {
				return fmt.Errorf("%s: dependency %s is the plan's Implementation Epic", c.Key, d)
			}
			is, err := a.f.Issue(ctx, n)
			switch {
			case errors.Is(err, ErrNotFound):
				return fmt.Errorf("%s: dependency %s does not exist", c.Key, d)
			case err != nil:
				return fmt.Errorf("%s: fetching dependency %s: %w", c.Key, d, err)
			case is.PR:
				return fmt.Errorf("%s: dependency %s is a pull request, not an issue", c.Key, d)
			}
		}
	}
	return nil
}

func issueRef(d string) (int64, bool) {
	if !issueRefRE.MatchString(d) {
		return 0, false
	}
	n, err := strconv.ParseInt(d[1:], 10, 64)
	return n, err == nil
}

// planChildren renders each child issue and decides what to write.
func (a *applier) planChildren() {
	for _, c := range a.children {
		c.content = a.childContent(c)
		c.sha = Hash(c.Title, c.content)
		if c.cur == nil {
			a.change("create %s in milestone %q: %s", c.ref(), a.rep.Milestone.Title, c.Title)
			continue
		}
		m, content, _ := ParseMarker(c.cur.Body)
		switch {
		case m.SHA == c.sha:
		case c.cur.Closed:
			a.flag("%s: issue is closed; plan text change skipped", c.ref())
		case Hash(c.cur.Title, content) != m.SHA && !a.o.Force:
			a.flag("%s: modified in Forgejo since last apply; plan text change skipped (--force overwrites it)", c.ref())
		default:
			c.update = true
			a.change("update %s: plan text changed", c.ref())
		}
	}
}

func (a *applier) link(path string) string {
	return fmt.Sprintf("/%s/%s/src/branch/%s/%s", a.o.Org, a.o.Repo, a.branch, path)
}

func (a *applier) childContent(c *child) string {
	var b strings.Builder
	b.WriteString(c.Body)
	fmt.Fprintf(&b, "\n\n---\n\n_Filed by `bottown milestone apply` from [`%s`](%s) for kickoff #%d.",
		a.file, a.link(a.file), a.plan.Kickoff)
	if d := a.plan.Design; d != "" {
		fmt.Fprintf(&b, " Design: [`%s`](%s).", d, a.link(d))
	}
	b.WriteString("_")
	return b.String()
}

// planEpic renders the epic, with "#?" for issues not created yet, and
// decides whether to write it.
func (a *applier) planEpic() {
	content := a.epicContent()
	body := withMarker(content, Marker{Plan: a.o.Slug, Epic: true, SHA: Hash(a.plan.Title, content)})
	switch {
	case a.epic == nil:
		a.epicNew = true
		a.change("create the Implementation Epic in milestone %q: %s", a.rep.Milestone.Title, a.plan.Title)
	case a.epic.Title != a.plan.Title || normalize(a.epic.Body) != body:
		a.epicNew = true
		a.change("update the Implementation Epic #%d", a.epic.Number)
	}
}

func (a *applier) epicContent() string {
	num := func(c *child) string {
		if c.cur == nil {
			return "#?"
		}
		return fmt.Sprintf("#%d", c.cur.Number)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "_Implementation Epic for kickoff #%d, filed by `bottown milestone apply` from [`%s`](/%s/%s/src/commit/%s/%s) at `%s`.",
		a.plan.Kickoff, a.file, a.o.Org, a.o.Repo, a.rep.PlanSHA, a.file, short(a.rep.PlanSHA))
	if d := a.plan.Design; d != "" {
		fmt.Fprintf(&b, " Design: [`%s`](%s).", d, a.link(d))
	}
	b.WriteString("_\n\n")
	if a.plan.Overview != "" {
		b.WriteString(a.plan.Overview + "\n\n")
	}
	b.WriteString("## Implementation issues\n\n")
	for _, c := range a.children {
		box := " "
		if c.cur != nil && c.cur.Closed {
			box = "x"
		}
		fmt.Fprintf(&b, "- [%s] %s — %s (`%s`", box, num(c), c.Title, c.Key)
		if len(c.Depends) > 0 {
			ds := make([]string, len(c.Depends))
			for i, d := range c.Depends {
				if dc, ok := a.byKey[d]; ok {
					ds[i] = num(dc)
				} else {
					ds[i] = d
				}
			}
			fmt.Fprintf(&b, ", depends on %s", strings.Join(ds, ", "))
		}
		b.WriteString(")\n")
	}
	if len(a.removed) > 0 {
		b.WriteString("\n### Flagged (removed from plan)\n\n")
		for _, is := range a.removed {
			m, _, _ := ParseMarker(is.Body)
			fmt.Fprintf(&b, "- #%d — %s (`%s`, %s): no longer in the plan\n", is.Number, is.Title, m.Key, is.state())
		}
	}
	return b.String()
}

var checklistRE = regexp.MustCompile("^- \\[[ x]\\] #[0-9]+ — .* \\(`([a-z0-9-]+)`(?:, depends on (#[0-9]+(?:, #[0-9]+)*))?\\)$")

// declared reads the dependencies the epic's checklist says each key had
// at the last apply.
func declared(epicBody string) map[string][]int64 {
	out := map[string][]int64{}
	for _, l := range strings.Split(normalize(epicBody), "\n") {
		sub := checklistRE.FindStringSubmatch(l)
		if sub == nil {
			continue
		}
		var ns []int64
		if sub[2] != "" {
			for _, d := range strings.Split(sub[2], ", ") {
				if n, ok := issueRef(d); ok {
					ns = append(ns, n)
				}
			}
		}
		out[sub[1]] = ns
	}
	return out
}

// A node in the dependency graph: a plan key ("k:<key>"), the epic ("e"),
// or any other issue ("n:<number>").
type node string

const epicNode node = "e"

func keyNode(k string) node { return node("k:" + k) }
func numNode(n int64) node  { return node("n:" + strconv.FormatInt(n, 10)) }
func (n node) key() string  { k, _ := strings.CutPrefix(string(n), "k:"); return k }
func (n node) isPlan() bool { return n == epicNode || strings.HasPrefix(string(n), "k:") }
func (n node) number() int64 {
	v, _ := strconv.ParseInt(strings.TrimPrefix(string(n), "n:"), 10, 64)
	return v
}

// nodeOf is the node for issue n.
func (a *applier) nodeOf(n int64) node {
	if a.epic != nil && a.epic.Number == n {
		return epicNode
	}
	for _, c := range a.children {
		if c.cur != nil && c.cur.Number == n {
			return keyNode(c.Key)
		}
	}
	return numNode(n)
}

func (a *applier) describe(n node) string {
	switch {
	case n == epicNode && a.epic == nil:
		return "the epic"
	case n == epicNode:
		return fmt.Sprintf("epic #%d", a.epic.Number)
	case n.isPlan():
		return a.byKey[n.key()].ref()
	}
	return fmt.Sprintf("#%d", n.number())
}

// resolve is the issue number of a node, once the issues exist.
func (a *applier) resolve(n node) int64 {
	switch {
	case n == epicNode:
		return a.epic.Number
	case n.isPlan():
		return a.byKey[n.key()].cur.Number
	}
	return n.number()
}

func (a *applier) fetchBlockedBy(ctx context.Context, n int64) ([]int64, error) {
	if deps, ok := a.blockedBy[n]; ok {
		return deps, nil
	}
	deps, err := a.f.BlockedBy(ctx, n)
	if err != nil {
		return nil, fmt.Errorf("fetching #%d's dependencies: %w", n, err)
	}
	a.blockedBy[n] = deps
	return deps, nil
}

// planDeps works out the blocked_by edges to add and remove, and checks
// that the graph they make with the forge's other edges has no cycle.
func (a *applier) planDeps(ctx context.Context) error {
	// Issues whose edges the plan owns: its children, removed ones, the epic.
	managed := map[node]bool{epicNode: true}
	for _, c := range a.children {
		managed[keyNode(c.Key)] = true
	}
	for _, is := range a.removed {
		managed[numNode(is.Number)] = true
	}
	var prev map[string][]int64
	if a.epic != nil {
		prev = declared(a.epic.Body)
	}

	final := map[node][]node{} // the plan nodes' edges after apply
	reconcile := func(from node, cur *ForgeIssue, want []node, owned func(node) bool) error {
		var have []node
		if cur != nil {
			deps, err := a.fetchBlockedBy(ctx, cur.Number)
			if err != nil {
				return err
			}
			for _, d := range deps {
				have = append(have, a.nodeOf(d))
			}
		}
		ch := depChange{node: from}
		edges := slices.Clone(want)
		for _, h := range have {
			switch {
			case slices.Contains(want, h):
			case owned(h):
				ch.remove = append(ch.remove, h)
			default:
				edges = append(edges, h) // the owner's own edge: kept
			}
		}
		for _, w := range want {
			if !slices.Contains(have, w) {
				ch.add = append(ch.add, w)
			}
		}
		final[from] = edges
		if len(ch.add)+len(ch.remove) > 0 {
			a.deps = append(a.deps, ch)
			for _, d := range ch.remove {
				a.change("%s: remove blocked by %s", a.describe(from), a.describe(d))
			}
			for _, d := range ch.add {
				a.change("%s: add blocked by %s", a.describe(from), a.describe(d))
			}
		}
		return nil
	}

	for _, c := range a.children {
		var want []node
		for _, d := range c.Depends {
			if n, ok := issueRef(d); ok {
				want = append(want, a.nodeOf(n))
			} else {
				want = append(want, keyNode(d))
			}
		}
		// Edges to issues outside the plan are the owner's, unless an
		// earlier version of the plan declared them.
		was := prev[c.Key]
		owned := func(n node) bool {
			return managed[n] || !n.isPlan() && slices.Contains(was, n.number())
		}
		if err := reconcile(keyNode(c.Key), c.cur, want, owned); err != nil {
			return err
		}
	}
	var all []node
	for _, c := range a.children {
		all = append(all, keyNode(c.Key))
	}
	if err := reconcile(epicNode, a.epic, all, func(n node) bool { return managed[n] }); err != nil {
		return err
	}
	return a.acyclic(ctx, final)
}

// acyclic looks for a cycle through the plan's issues in the plan's edges
// together with the forge's edges between other issues.
func (a *applier) acyclic(ctx context.Context, final map[node][]node) error {
	edges := func(n node) ([]node, error) {
		if e, ok := final[n]; ok {
			return e, nil
		}
		deps, err := a.fetchBlockedBy(ctx, n.number())
		if err != nil {
			return nil, err
		}
		out := make([]node, len(deps))
		for i, d := range deps {
			out[i] = a.nodeOf(d)
		}
		return out, nil
	}
	const (
		unseen = iota
		onPath
		done
	)
	state := map[node]int{}
	var stack []node
	var visit func(n node) error
	visit = func(n node) error {
		state[n] = onPath
		stack = append(stack, n)
		next, err := edges(n)
		if err != nil {
			return err
		}
		for _, d := range next {
			switch state[d] {
			case unseen:
				if err := visit(d); err != nil {
					return err
				}
			case onPath:
				i := slices.Index(stack, d)
				cycle := append(slices.Clone(stack[i:]), d)
				if slices.ContainsFunc(cycle, node.isPlan) {
					names := make([]string, len(cycle))
					for j, c := range cycle {
						names[j] = a.describe(c)
					}
					return fmt.Errorf("the plan's dependencies make a cycle with the forge's: %s (each blocked by the next)", strings.Join(names, " -> "))
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[n] = done
		return nil
	}
	for _, c := range a.children {
		if n := keyNode(c.Key); state[n] == unseen {
			if err := visit(n); err != nil {
				return err
			}
		}
	}
	if state[epicNode] == unseen {
		return visit(epicNode)
	}
	return nil
}

func (a *applier) change(format string, args ...any) {
	a.rep.Changes = append(a.rep.Changes, fmt.Sprintf(format, args...))
}

func (a *applier) flag(format string, args ...any) {
	a.rep.Flags = append(a.rep.Flags, fmt.Sprintf(format, args...))
}

func (a *applier) print() {
	w, rep := a.o.Out, a.rep
	fmt.Fprintf(w, "plan %s on %s at %s (last changed in %s)\n", a.file, a.branch, short(rep.MainSHA), short(rep.PlanSHA))
	fmt.Fprintf(w, "milestone %q (id %d), kickoff #%d\n", rep.Milestone.Title, rep.Milestone.ID, a.plan.Kickoff)
	if len(rep.Changes) == 0 {
		fmt.Fprintln(w, "no changes")
	}
	for _, c := range rep.Changes {
		fmt.Fprintln(w, "  "+c)
	}
	for _, f := range rep.Flags {
		fmt.Fprintln(w, "flagged: "+f)
	}
}

// execute makes the planned writes: child issues, then the epic and the
// dependencies, so that each step can name the issues the earlier made.
func (a *applier) execute(ctx context.Context) error {
	f, w := a.f, a.o.Out
	marker := func(c *child) string {
		return withMarker(c.content, Marker{Plan: a.o.Slug, Key: c.Key, SHA: c.sha})
	}
	for _, c := range a.children {
		switch {
		case c.cur == nil:
			n, err := f.CreateIssue(ctx, c.Title, marker(c), a.rep.Milestone.ID)
			if err != nil {
				return fmt.Errorf("creating the issue for %s: %w", c.Key, err)
			}
			a.rep.Writes++
			c.cur = &ForgeIssue{Number: n, Title: c.Title, Body: marker(c), Poster: a.o.Owner, Milestone: &a.rep.Milestone}
			fmt.Fprintf(w, "created %s\n", c.ref())
		case c.update:
			if err := f.EditIssue(ctx, c.cur.Number, c.Title, marker(c)); err != nil {
				return fmt.Errorf("updating %s: %w", c.ref(), err)
			}
			a.rep.Writes++
			fmt.Fprintf(w, "updated %s\n", c.ref())
		}
	}
	epicBody := func() string {
		content := a.epicContent()
		return withMarker(content, Marker{Plan: a.o.Slug, Epic: true, SHA: Hash(a.plan.Title, content)})
	}
	// A new epic is created before the dependencies, since it has some.
	if a.epicNew && a.epic == nil {
		body := epicBody()
		n, err := f.CreateIssue(ctx, a.plan.Title, body, a.rep.Milestone.ID)
		if err != nil {
			return fmt.Errorf("creating the Implementation Epic: %w", err)
		}
		a.epic = &ForgeIssue{Number: n, Title: a.plan.Title, Body: body, Poster: a.o.Owner, Milestone: &a.rep.Milestone}
		a.rep.Epic = n
		a.rep.Writes++
		a.epicNew = false
		fmt.Fprintf(w, "created the Implementation Epic #%d\n", n)
	}
	// Removals before additions, so that reversing an edge never makes a
	// transient cycle Forgejo would refuse.
	for _, ch := range a.deps {
		for _, d := range ch.remove {
			from, to := a.resolve(ch.node), a.resolve(d)
			if err := f.RemoveBlockedBy(ctx, from, to); err != nil {
				return fmt.Errorf("removing #%d's dependency on #%d: %w", from, to, err)
			}
			a.rep.Writes++
			fmt.Fprintf(w, "#%d: no longer blocked by #%d\n", from, to)
		}
	}
	// An existing epic is updated only after the removals: its checklist
	// records the external dependencies the plan declared, which tells
	// stale edges from the owner's own, so an apply interrupted before
	// then still finds the stale ones on the next.
	if a.epicNew {
		if err := f.EditIssue(ctx, a.epic.Number, a.plan.Title, epicBody()); err != nil {
			return fmt.Errorf("updating the Implementation Epic #%d: %w", a.epic.Number, err)
		}
		a.rep.Writes++
		fmt.Fprintf(w, "updated the Implementation Epic #%d\n", a.epic.Number)
	}
	for _, ch := range a.deps {
		for _, d := range ch.add {
			from, to := a.resolve(ch.node), a.resolve(d)
			if err := f.AddBlockedBy(ctx, from, to); err != nil {
				return fmt.Errorf("making #%d blocked by #%d: %w", from, to, err)
			}
			a.rep.Writes++
			fmt.Fprintf(w, "#%d: blocked by #%d\n", from, to)
		}
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
