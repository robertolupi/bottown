package webui

import (
	"net/http"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/secrets"
	serverapi "github.com/robertolupi/bottown/internal/server"
)

// fakePRList is what the fixture servers' GET /v1/forge/prs returns: the
// fixture's PRs in the requested state, each open one assessed from its
// fixture reviews as the server assesses them (opPRList), so the web UI
// tests keep describing PRs by their Forgejo reviews.
type fakePRList struct {
	prs        []*sdk.PullRequest
	reviews    map[int64][]*sdk.PullReview
	carries    map[int64][]review.Carry
	reviewsErr map[int64]int // a review read that fails leaves the state unknown
	families   map[string]string
	owner      string
	runs       []secrets.Run
	repo       string
}

func (f fakePRList) items(r *http.Request) []serverapi.PRItem {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
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
			head := ""
			if pr.Head != nil {
				head = pr.Head.Sha
			}
			a := review.Assess(head, review.FromForge(f.reviews[pr.Index]), review.Policy{
				Author: userLogin(pr.Poster), Owner: f.owner, Families: f.families,
				Grants: review.Grants(f.runs, f.repo, pr.Index), Carries: f.carries[pr.Index],
			})
			item.ReviewState, item.ReviewReason, item.Assessment = string(a.Decision), a.Reason, &a
		}
		out = append(out, item)
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
