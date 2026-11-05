package webui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/secrets"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// fakeAPI answers the server reads the web UI makes (internal/webui
// forge.go) from Forgejo-shaped fixture data, building each answer with the
// server's own constructors (NewPRItem, NewIssueItem, Run.State) and
// assessing reviews as the server does (opPRList), so the tests keep
// describing the forge as Forgejo would. The fixture servers delegate to it
// and serve no raw /forgejo/api/v1 lists, so a raw read left in the UI
// fails its tests.
type fakeAPI struct {
	prs        []*sdk.PullRequest
	reviews    map[int64][]*sdk.PullReview
	carries    map[int64][]review.Carry
	reviewsErr map[int64]int // a failed review read leaves the review state unknown
	pullsErr   int           // the PR list fails with this status
	statuses   map[string][]*sdk.Status
	issues     []*sdk.Issue
	comments   map[int64][]*sdk.Comment
	deps       map[int64][]int64
	milestones []*sdk.Milestone
	branches   map[string]serverapi.BranchOut
	files      map[string]string // by ref, the .bottown.toml there
	fileRefs   *[]string         // refs the file read was asked for
	runs       []secrets.Run
	families   map[string]string
	owner      string
}

// serve answers r if it is one of the reads, and reports whether it did.
func (f fakeAPI) serve(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	num, _ := strconv.ParseInt(q.Get("number"), 10, 64)
	var out any
	switch r.URL.Path {
	case "/v1/runs":
		out = f.runList(r)
	case "/v1/forge/prs":
		if f.pullsErr > 0 {
			http.Error(w, "pulls error", f.pullsErr)
			return true
		}
		out = f.prList(r)
	case "/v1/forge/pr":
		pr := f.pr(num)
		if pr == nil {
			http.NotFound(w, r)
			return true
		}
		out = f.prOut(r, pr)
	case "/v1/forge/issues":
		items := []serverapi.IssueItem{}
		state := q.Get("state")
		for _, is := range f.issues {
			if is.PullRequest == nil && (state == "all" || string(is.State) == cmpState(state)) {
				items = append(items, serverapi.NewIssueItem(is, q.Get("body") == "1"))
			}
		}
		out = items
	case "/v1/forge/issue":
		is := f.issue(num)
		if is == nil {
			http.NotFound(w, r)
			return true
		}
		item := serverapi.NewIssueItem(is, true)
		o := serverapi.IssueOut{Number: item.Number, Title: item.Title, Body: item.Body, State: item.State, Author: item.Author,
			Labels: item.Labels, URL: item.URL, Created: item.Created, Milestone: item.Milestone}
		if q.Get("comments") != "0" {
			o.Comments = []serverapi.CommentOut{}
			for _, c := range f.comments[num] {
				o.Comments = append(o.Comments, serverapi.CommentOut{ID: c.ID, Author: userLogin(c.Poster), Body: c.Body, Created: c.Created})
			}
		}
		out = o
	case "/v1/forge/issue/dependencies":
		items := []serverapi.IssueItem{}
		for _, d := range f.deps[num] {
			if is := f.issue(d); is != nil {
				items = append(items, serverapi.NewIssueItem(is, true))
			}
		}
		out = items
	case "/v1/forge/milestones":
		items := []serverapi.MilestoneItem{}
		for _, m := range f.milestones {
			items = append(items, serverapi.MilestoneItem{ID: m.ID, Title: m.Title, State: string(m.State)})
		}
		out = items
	case "/v1/forge/branch":
		b, ok := f.branches[q.Get("name")]
		if !ok {
			http.NotFound(w, r)
			return true
		}
		out = b
	case "/v1/forge/file":
		if f.fileRefs != nil {
			*f.fileRefs = append(*f.fileRefs, q.Get("ref"))
		}
		c, ok := f.files[q.Get("ref")]
		if !ok || q.Get("path") != ".bottown.toml" {
			http.NotFound(w, r)
			return true
		}
		_, _ = w.Write([]byte(c))
		return true
	default:
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
	return true
}

func cmpState(state string) string {
	if state == "" {
		return string(sdk.StateOpen)
	}
	return state
}

func (f fakeAPI) runList(r *http.Request) []serverapi.RunItem {
	q := r.URL.Query()
	state, history := q.Get("state"), q.Get("history") == "1"
	now := time.Now().UTC()
	out := []serverapi.RunItem{}
	for _, run := range f.runs {
		st := run.State(now)
		switch {
		case run.Purged && !history,
			state == "live" && run.Ended != nil,
			state != "" && state != "live" && st != state:
			continue
		}
		out = append(out, serverapi.RunItem{Run: run, State: st})
	}
	return out
}

func (f fakeAPI) pr(n int64) *sdk.PullRequest {
	for _, pr := range f.prs {
		if pr.Index == n {
			return pr
		}
	}
	return nil
}

func (f fakeAPI) issue(n int64) *sdk.Issue {
	for _, is := range f.issues {
		if is.Index == n {
			return is
		}
	}
	return nil
}

func (f fakeAPI) assess(repo string, pr *sdk.PullRequest) review.Assessment {
	head := ""
	if pr.Head != nil {
		head = pr.Head.Sha
	}
	return review.Assess(head, review.FromForge(f.reviews[pr.Index]), review.Policy{
		Author: userLogin(pr.Poster), Owner: f.owner, Families: f.families,
		Grants: review.Grants(f.runs, repo, pr.Index), Carries: f.carries[pr.Index],
	})
}

func (f fakeAPI) prList(r *http.Request) []serverapi.PRItem {
	q := r.URL.Query()
	state := cmpState(q.Get("state"))
	out := []serverapi.PRItem{}
	for _, pr := range f.prs {
		open := pr.State != sdk.StateClosed && !pr.HasMerged
		if state == "open" && !open || state == "closed" && open {
			continue
		}
		item := serverapi.NewPRItem(pr)
		switch {
		case pr.HasMerged:
			item.ReviewState = "merged"
		case !open:
			item.ReviewState = "closed"
		case f.reviewsErr[pr.Index] > 0:
			item.ReviewState = "unknown"
		default:
			a := f.assess(q.Get("repo"), pr)
			item.ReviewState, item.ReviewReason, item.Assessment = string(a.Decision), a.Reason, &a
			item.ChangesRequested = serverapi.ChangesRequested(f.reviewOuts(pr.Index))
		}
		if open && q.Get("checks") == "1" && item.HeadSHA != "" {
			item.Checks = f.checks(item.HeadSHA)
		}
		out = append(out, item)
	}
	return out
}

func (f fakeAPI) prOut(r *http.Request, pr *sdk.PullRequest) serverapi.PROut {
	item := serverapi.NewPRItem(pr)
	o := serverapi.PROut{Number: item.Number, Title: item.Title, State: item.State, Merged: item.Merged, Author: item.Author,
		HeadBranch: item.HeadBranch, HeadSHA: item.HeadSHA, BaseBranch: item.BaseBranch, Mergeable: item.Mergeable,
		Reviews: f.reviewOuts(pr.Index), Assessment: f.assess(r.URL.Query().Get("repo"), pr), Checks: f.checks(item.HeadSHA)}
	return o
}

func (f fakeAPI) reviewOuts(n int64) []serverapi.ReviewOut {
	out := []serverapi.ReviewOut{}
	for _, rv := range f.reviews[n] {
		out = append(out, serverapi.ReviewOut{ID: rv.ID, Reviewer: userLogin(rv.Reviewer), State: string(rv.State), CommitID: rv.CommitID,
			Body: rv.Body, Stale: rv.Stale, Dismissed: rv.Dismissed, Official: rv.Official, Submitted: rv.Submitted})
	}
	return out
}

// checks are the newest status per context and creator at sha, the owner's
// marked as the gate, as the server reports them.
func (f fakeAPI) checks(sha string) []serverapi.CommitStatus {
	type key struct{ context, creator string }
	latest := map[key]*sdk.Status{}
	for _, st := range f.statuses[sha] {
		k := key{st.Context, userLogin(st.Creator)}
		if cur, ok := latest[k]; !ok || st.Created.After(cur.Created) || st.Created.Equal(cur.Created) && st.ID > cur.ID {
			latest[k] = st
		}
	}
	out := []serverapi.CommitStatus{}
	for k, st := range latest {
		out = append(out, serverapi.CommitStatus{Context: st.Context, State: string(st.State), Description: st.Description,
			Creator: k.creator, Created: st.Created, Gate: f.owner != "" && k.creator == f.owner})
	}
	return out
}

// familiesOf maps the fixtures' actor entries ({"name", "family"}) to
// their families.
func familiesOf(actors []map[string]any) map[string]string {
	out := map[string]string{}
	for _, a := range actors {
		name, _ := a["name"].(string)
		family, _ := a["family"].(string)
		out[name] = family
	}
	return out
}
