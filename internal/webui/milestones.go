package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/secrets"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// Node statuses in the Milestones view (docs/design/milestones.md §2.7).
const (
	NodeBlocked  = "blocked"   // waiting on open dependencies
	NodeReady    = "ready"     // dependencies closed, no run, PR or stop
	NodeRunning  = "running"   // a live run, or a flow between its runs
	NodeInReview = "in_review" // an open PR without a verdict at its head
	NodeApproved = "approved"  // an open PR whose reviews assess as approved
	NodeStopped  = "stopped"   // the last flow stopped, or the last run failed
	NodeClosed   = "closed"
)

// MilestoneGroup is one card of the Milestones view: an Implementation Epic
// with its active children, or a Forgejo milestone without a plan epic and
// its issues.
type MilestoneGroup struct {
	ID        string          `json:"id"`
	Milestone *MilestoneRef   `json:"milestone,omitempty"`
	Epic      *IssueRef       `json:"epic,omitempty"`
	Plan      string          `json:"plan,omitempty"`
	PlanFile  string          `json:"plan_file,omitempty"`
	Kickoff   *IssueRef       `json:"kickoff,omitempty"`
	Design    string          `json:"design,omitempty"`
	DesignURL string          `json:"design_url,omitempty"`
	Base      string          `json:"base"`
	Removed   []IssueRef      `json:"removed"`
	Counts    MilestoneCounts `json:"counts"`
	Nodes     []MilestoneNode `json:"nodes"`
	Edges     []MilestoneEdge `json:"edges"`
	Layers    int             `json:"layers"`
	Error     string          `json:"error,omitempty"`
	members   []int64
	deps      map[int64][]int64
}

// MilestoneRef is a Forgejo milestone.
type MilestoneRef struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}

// IssueRef is an issue the view links to.
type IssueRef struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Key    string `json:"key,omitempty"`
}

// MilestoneCounts are a group's progress counts over its member nodes.
// AwaitingMerge counts the approved ones.
type MilestoneCounts struct {
	Total         int `json:"total"`
	Closed        int `json:"closed"`
	Ready         int `json:"ready"`
	Running       int `json:"running"`
	InReview      int `json:"in_review"`
	AwaitingMerge int `json:"awaiting_merge"`
	Blocked       int `json:"blocked"`
	Stopped       int `json:"stopped"`
}

// MilestoneNode is one issue of the DAG. External nodes are dependencies
// outside the group, shown as pills: they carry no status beyond State.
type MilestoneNode struct {
	Number     int64          `json:"number"`
	Title      string         `json:"title"`
	URL        string         `json:"url"`
	State      string         `json:"state"`
	Key        string         `json:"key,omitempty"`
	External   bool           `json:"external,omitempty"`
	Status     string         `json:"status"`
	Layer      int            `json:"layer"`
	DependsOn  []int64        `json:"depends_on"`
	Implement  string         `json:"implement,omitempty"`
	Verify     string         `json:"verify,omitempty"`
	Run        *NodeRun       `json:"run,omitempty"`
	PR         *NodePR        `json:"pr,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	Flow       *ActiveFlowRef `json:"flow,omitempty"`
}

// NodeRun is a node's live run.
type NodeRun struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Actor   string    `json:"actor"`
	Created time.Time `json:"created"`
	Elapsed string    `json:"elapsed"`
}

// NodePR is a node's pull request: its open one, or else its newest
// merged one.
type NodePR struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	State  string `json:"state"` // open, merged, closed
	Review string `json:"review,omitempty"`
}

// ActiveFlowRef is the UI-started flow supervising a node.
type ActiveFlowRef struct {
	Implement string `json:"implement"`
	Verify    string `json:"verify"`
}

// MilestoneEdge says To depends on From: From ──▶ To.
type MilestoneEdge struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// flowStopRE matches the comment internal/flow posts when it stops.
var flowStopRE = regexp.MustCompile(`^\*\*bottown flow: ([^*\n]+)\*\*`)

// ComputeMilestones builds the Milestones view (docs/design/milestones.md
// §2.7) from forge and run state. Markers count only on the owner's issues
// (§2.6).
func (s *Server) ComputeMilestones(ctx context.Context) ([]MilestoneGroup, error) {
	org, repo := s.orgAndRepo()
	forgeURL := s.cfg.Instance.ForgejoURL()
	repoAPI := fmt.Sprintf("/forgejo/api/v1/repos/%s/%s", org, repo)
	repoWeb := fmt.Sprintf("%s/%s/%s", forgeURL, org, repo)

	ownerLogin := s.resolveOwnerLogin(ctx)
	issues, err := listAll[*sdk.Issue](ctx, s, repoAPI+"/issues?state=all&type=issues")
	if err != nil {
		return nil, fmt.Errorf("fetch issues: %w", err)
	}
	milestones, err := listAll[*sdk.Milestone](ctx, s, repoAPI+"/milestones?state=all")
	if err != nil {
		return nil, fmt.Errorf("fetch milestones: %w", err)
	}
	prs, err := s.pullRequests(ctx, "all")
	if err != nil {
		return nil, fmt.Errorf("fetch pull requests: %w", err)
	}
	var allRuns []secrets.Run
	if err := s.callServer(ctx, http.MethodGet, "/v1/runs?history=1&repo="+url.QueryEscape(repo), nil, &allRuns); err != nil {
		return nil, fmt.Errorf("fetch runs: %w", err)
	}

	byNumber := make(map[int64]*sdk.Issue, len(issues))
	for _, is := range issues {
		if is != nil && is.PullRequest == nil {
			byNumber[is.Index] = is
		}
	}
	issueURL := func(is *sdk.Issue) string {
		if is.HTMLURL != "" {
			return is.HTMLURL
		}
		return fmt.Sprintf("%s/issues/%d", repoWeb, is.Index)
	}
	ref := func(is *sdk.Issue) IssueRef {
		r := IssueRef{Number: is.Index, Title: is.Title, State: string(is.State), URL: issueURL(is)}
		if m, _, ok := milestone.ParseMarker(is.Body); ok && milestone.Trusted(userLogin(is.Poster), ownerLogin) {
			r.Key = m.Key
		}
		return r
	}
	msRef := func(m *sdk.Milestone) *MilestoneRef {
		if m == nil || m.ID <= 0 {
			return nil
		}
		return &MilestoneRef{ID: m.ID, Title: m.Title, State: string(m.State), URL: fmt.Sprintf("%s/milestone/%d", repoWeb, m.ID)}
	}
	depsOf := func(n int64) ([]int64, error) {
		list, err := listAll[*sdk.Issue](ctx, s, fmt.Sprintf("%s/issues/%d/dependencies", repoAPI, n))
		if err != nil {
			return nil, err
		}
		out := make([]int64, 0, len(list))
		for _, d := range list {
			if d != nil {
				out = append(out, d.Index)
				if byNumber[d.Index] == nil && d.PullRequest == nil {
					byNumber[d.Index] = d
				}
			}
		}
		return out, nil
	}

	// Trusted markers: epics by plan, children by plan.
	epics := map[string][]*sdk.Issue{}
	children := map[string][]*sdk.Issue{}
	for _, is := range issues {
		if is == nil || is.PullRequest != nil || !milestone.Trusted(userLogin(is.Poster), ownerLogin) {
			continue
		}
		m, _, ok := milestone.ParseMarker(is.Body)
		if !ok {
			continue
		}
		if m.Epic {
			epics[m.Plan] = append(epics[m.Plan], is)
		} else {
			children[m.Plan] = append(children[m.Plan], is)
		}
	}

	var groups []*MilestoneGroup
	withEpic := map[int64]bool{} // Forgejo milestones that hold an epic
	plans := make([]string, 0, len(epics))
	for p := range epics {
		plans = append(plans, p)
	}
	slices.Sort(plans)
	for _, plan := range plans {
		for _, epic := range epics[plan] {
			g := &MilestoneGroup{
				ID:        fmt.Sprintf("epic-%d", epic.Index),
				Milestone: msRef(epic.Milestone),
				Plan:      plan,
				deps:      map[int64][]int64{},
			}
			er := ref(epic)
			g.Epic = &er
			if g.Milestone != nil {
				withEpic[g.Milestone.ID] = true
			}
			if k, file, design, ok := milestone.EpicHeader(epic.Body); ok {
				g.PlanFile = file
				g.Kickoff = &IssueRef{Number: k, URL: fmt.Sprintf("%s/issues/%d", repoWeb, k)}
				if ki := byNumber[k]; ki != nil {
					kr := ref(ki)
					g.Kickoff = &kr
				}
				if design != "" {
					g.Design = design
					g.DesignURL = fmt.Sprintf("%s/src/branch/main/%s", repoWeb, design)
				}
			}
			// The active children are the plan's children the epic is
			// blocked by; the plan's other children were removed from it.
			active, err := depsOf(epic.Index)
			if err != nil {
				g.Error = fmt.Sprintf("dependencies of #%d: %v", epic.Index, err)
			}
			inEpic := map[int64]bool{}
			for _, n := range active {
				inEpic[n] = true
			}
			for _, c := range children[plan] {
				if inEpic[c.Index] {
					g.members = append(g.members, c.Index)
				} else {
					g.Removed = append(g.Removed, ref(c))
				}
			}
			groups = append(groups, g)
		}
	}
	// Open Forgejo milestones without a plan epic, with their issues.
	for _, m := range milestones {
		if m == nil || withEpic[m.ID] || m.State == sdk.StateClosed {
			continue
		}
		g := &MilestoneGroup{
			ID:        fmt.Sprintf("milestone-%d", m.ID),
			Milestone: msRef(m),
			deps:      map[int64][]int64{},
		}
		for _, is := range issues {
			if is != nil && is.PullRequest == nil && is.Milestone != nil && is.Milestone.ID == m.ID {
				g.members = append(g.members, is.Index)
			}
		}
		groups = append(groups, g)
	}

	// Branches: an epic's base is milestone/<E> once it exists (§3.3).
	for _, g := range groups {
		g.Base = "main"
		if g.Epic != nil {
			var br sdk.Branch
			if err := s.callServer(ctx, http.MethodGet, fmt.Sprintf("%s/branches/milestone/%d", repoAPI, g.Epic.Number), nil, &br); err == nil && br.Name != "" {
				g.Base = br.Name
			}
		}
	}

	// Pull requests by issue: the open one, else the newest merged one.
	prByIssue := map[int64]*serverapi.PRItem{}
	for _, pr := range prs {
		if pr == nil {
			continue
		}
		n := parseIssueRef(pr.HeadBranch)
		if n <= 0 {
			continue
		}
		cur := prByIssue[n]
		switch {
		case isPROpen(pr):
			if cur == nil || !isPROpen(cur) || pr.Number > cur.Number {
				prByIssue[n] = pr
			}
		case pr.Merged && (cur == nil || (!isPROpen(cur) && pr.Number > cur.Number)):
			prByIssue[n] = pr
		}
	}

	now := time.Now()
	for _, g := range groups {
		s.buildGraph(ctx, g, byNumber, depsOf, ref, prByIssue, allRuns, ownerLogin, repoAPI, repoWeb, now)
	}

	out := make([]MilestoneGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	// Open groups first, then by milestone, then by epic.
	slices.SortStableFunc(out, func(a, b MilestoneGroup) int {
		if ao, bo := a.open(), b.open(); ao != bo {
			if ao {
				return -1
			}
			return 1
		}
		return strings.Compare(a.sortKey(), b.sortKey())
	})
	return out, nil
}

func (g *MilestoneGroup) open() bool {
	if g.Epic != nil {
		return g.Epic.State != string(sdk.StateClosed)
	}
	return g.Milestone == nil || g.Milestone.State != string(sdk.StateClosed)
}

func (g *MilestoneGroup) sortKey() string {
	var ms, ep int64
	if g.Milestone != nil {
		ms = g.Milestone.ID
	}
	if g.Epic != nil {
		ep = g.Epic.Number
	}
	return fmt.Sprintf("%012d/%012d", ms, ep)
}

// buildGraph fills in a group's nodes, edges, layers, statuses and counts.
func (s *Server) buildGraph(
	ctx context.Context,
	g *MilestoneGroup,
	byNumber map[int64]*sdk.Issue,
	depsOf func(int64) ([]int64, error),
	ref func(*sdk.Issue) IssueRef,
	prByIssue map[int64]*serverapi.PRItem,
	allRuns []secrets.Run,
	ownerLogin, repoAPI, repoWeb string,
	now time.Time,
) {
	g.Nodes, g.Edges = []MilestoneNode{}, []MilestoneEdge{}
	if g.Removed == nil {
		g.Removed = []IssueRef{}
	}
	slices.Sort(g.members)
	member := map[int64]bool{}
	for _, n := range g.members {
		member[n] = true
	}

	var externals []int64
	seenExternal := map[int64]bool{}
	for _, n := range g.members {
		deps, err := depsOf(n)
		if err != nil && g.Error == "" {
			g.Error = fmt.Sprintf("dependencies of #%d: %v", n, err)
		}
		g.deps[n] = deps
		for _, d := range deps {
			g.Edges = append(g.Edges, MilestoneEdge{From: d, To: n})
			if !member[d] && !seenExternal[d] {
				seenExternal[d] = true
				externals = append(externals, d)
			}
		}
	}
	slices.Sort(externals)
	layers := layerize(append(slices.Clone(g.members), externals...), g.deps)

	for _, n := range externals {
		node := MilestoneNode{Number: n, URL: fmt.Sprintf("%s/issues/%d", repoWeb, n), External: true, Layer: layers[n], DependsOn: []int64{}}
		if is := byNumber[n]; is != nil {
			r := ref(is)
			node.Title, node.URL, node.State = r.Title, r.URL, r.State
		}
		node.Status = node.State
		g.Nodes = append(g.Nodes, node)
	}

	// Runs by issue and by PR, oldest first.
	runsFor := func(issue int64, pr *serverapi.PRItem) []secrets.Run {
		var out []secrets.Run
		for _, r := range allRuns {
			if r.Kind != "implement" && r.Kind != "verify" {
				continue
			}
			if r.Issue == issue || parseIssueRef(r.Branch) == issue || (pr != nil && r.PR == pr.Number) {
				out = append(out, r)
			}
		}
		slices.SortStableFunc(out, func(a, b secrets.Run) int { return a.Created.Compare(b.Created) })
		return out
	}

	for _, n := range g.members {
		is := byNumber[n]
		if is == nil {
			continue
		}
		r := ref(is)
		node := MilestoneNode{
			Number: n, Title: r.Title, URL: r.URL, State: r.State, Key: r.Key,
			Layer: layers[n], DependsOn: g.deps[n],
		}
		if node.DependsOn == nil {
			node.DependsOn = []int64{}
		}
		for _, l := range is.Labels {
			if l == nil {
				continue
			}
			if a, ok := strings.CutPrefix(l.Name, "implement/"); ok {
				node.Implement = a
			}
			if a, ok := strings.CutPrefix(l.Name, "verify/"); ok {
				node.Verify = a
			}
		}

		pr := prByIssue[n]
		if pr != nil {
			state := "closed"
			switch {
			case isPROpen(pr):
				state = "open"
			case pr.Merged:
				state = "merged"
			}
			u := pr.URL
			if u == "" {
				u = fmt.Sprintf("%s/pulls/%d", repoWeb, pr.Number)
			}
			node.PR = &NodePR{Number: pr.Number, Title: pr.Title, URL: u, State: state}
		}

		runs := runsFor(n, pr)
		var live *secrets.Run
		var lastImpl, lastVer string
		for i := range runs {
			r := &runs[i]
			if r.Ended == nil && (r.Expires.IsZero() || now.Before(r.Expires)) {
				live = r
			}
			if r.Kind == "implement" {
				lastImpl = r.Actor
			} else {
				lastVer = r.Actor
			}
		}
		if live != nil {
			node.Run = &NodeRun{ID: live.ID, Kind: live.Kind, Actor: live.Actor, Created: live.Created, Elapsed: formatDuration(now.Sub(live.Created))}
		}
		flowRec := s.getActiveFlow(n)
		if flowRec != nil {
			node.Flow = &ActiveFlowRef{Implement: flowRec.Implement, Verify: flowRec.Verify}
		}
		// Actor pair: labels, else the running flow, else the last runs.
		if node.Implement == "" && node.Verify == "" {
			if flowRec != nil {
				node.Implement, node.Verify = flowRec.Implement, flowRec.Verify
			} else {
				node.Implement, node.Verify = lastImpl, lastVer
			}
		}

		blocked := false
		for _, d := range node.DependsOn {
			if di := byNumber[d]; di == nil || di.State != sdk.StateClosed {
				blocked = true
			}
		}

		switch {
		case is.State == sdk.StateClosed:
			node.Status = NodeClosed
		case live != nil || flowRec != nil:
			node.Status = NodeRunning
		default:
			var lastRun *secrets.Run
			if len(runs) > 0 {
				lastRun = &runs[len(runs)-1]
			}
			reason, at := s.lastFlowStop(ctx, repoAPI, n, ownerLogin)
			var decision review.Decision
			if node.PR != nil && node.PR.State == "open" {
				decision = assessment(pr).Decision
				node.PR.Review = string(decision)
			}
			switch {
			case reason != "" && reason != "ready to merge" && (lastRun == nil || !lastRun.Created.After(at)):
				node.Status, node.StopReason = NodeStopped, reason
			case lastRun != nil && lastRun.Status == "failed":
				node.Status, node.StopReason = NodeStopped, "run failed"
			case decision == review.Approved:
				node.Status = NodeApproved
			case decision == review.ChangesRequested:
				node.Status, node.StopReason = NodeStopped, "changes requested"
			case decision == review.Commented:
				node.Status, node.StopReason = NodeStopped, "verifier undecided"
			case decision != "":
				node.Status = NodeInReview
			case blocked:
				node.Status = NodeBlocked
			default:
				node.Status = NodeReady
			}
		}
		g.Nodes = append(g.Nodes, node)

		g.Counts.Total++
		switch node.Status {
		case NodeClosed:
			g.Counts.Closed++
		case NodeReady:
			g.Counts.Ready++
		case NodeRunning:
			g.Counts.Running++
		case NodeInReview:
			g.Counts.InReview++
		case NodeApproved:
			g.Counts.AwaitingMerge++
		case NodeBlocked:
			g.Counts.Blocked++
		case NodeStopped:
			g.Counts.Stopped++
		}
	}

	for _, n := range g.Nodes {
		if n.Layer+1 > g.Layers {
			g.Layers = n.Layer + 1
		}
	}
	slices.SortStableFunc(g.Nodes, func(a, b MilestoneNode) int {
		if a.Layer != b.Layer {
			return a.Layer - b.Layer
		}
		return int(a.Number - b.Number)
	})
}

// lastFlowStop is the reason and time of the newest stop comment the flow
// (running as the owner) posted on an issue.
func (s *Server) lastFlowStop(ctx context.Context, repoAPI string, issue int64, ownerLogin string) (string, time.Time) {
	var comments []*sdk.Comment
	// Forgejo returns all issue comments and ignores page/limit.
	if err := s.callServer(ctx, http.MethodGet, fmt.Sprintf("%s/issues/%d/comments", repoAPI, issue), nil, &comments); err != nil {
		return "", time.Time{}
	}
	var reason string
	var at time.Time
	for _, c := range comments {
		if c == nil || userLogin(c.Poster) != ownerLogin {
			continue
		}
		if m := flowStopRE.FindStringSubmatch(c.Body); m != nil && !c.Created.Before(at) {
			reason, at = strings.TrimSpace(m[1]), c.Created
		}
	}
	return reason, at
}

// layerize assigns each node its longest distance from a root, following
// deps (n depends on deps[n]) among nodes only. Nodes on a cycle, which
// Forgejo and apply refuse, go after every other layer.
func layerize(nodes []int64, deps map[int64][]int64) map[int64]int {
	in := map[int64]bool{}
	for _, n := range nodes {
		in[n] = true
	}
	layer := map[int64]int{}
	remaining := slices.Clone(nodes)
	for len(remaining) > 0 {
		var next []int64
		progress := false
		for _, n := range remaining {
			l, ok := 0, true
			for _, d := range deps[n] {
				if !in[d] {
					continue
				}
				dl, done := layer[d]
				if !done {
					ok = false
					break
				}
				l = max(l, dl+1)
			}
			if ok {
				layer[n] = l
				progress = true
			} else {
				next = append(next, n)
			}
		}
		if !progress {
			top := 0
			for _, l := range layer {
				top = max(top, l+1)
			}
			for _, n := range next {
				layer[n] = top
			}
			break
		}
		remaining = next
	}
	return layer
}
