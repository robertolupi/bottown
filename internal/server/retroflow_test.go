package server

import (
	"testing"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/secrets"
)

func TestIssueBranchNumber(t *testing.T) {
	for ref, want := range map[string]int64{"issue/278": 278, "issue/0": 0, "issue/x": 0, "feature/3": 0, "3": 0} {
		if n, ok := IssueBranchNumber(ref); ok != (want > 0) || ok && n != want {
			t.Errorf("IssueBranchNumber(%q) = %d, %v; want %d", ref, n, ok, want)
		}
	}
}

// A retro on a pull request from issue/7 covers issue 7's flow, the same
// as a retro on #7; one on a PR from another branch covers that PR and
// its branch only.
func TestRetroFlow(t *testing.T) {
	rs := []secrets.Run{
		{ID: "impl", Kind: policy.Implement, Issue: 7, Branch: "issue/7"},
		{ID: "ver", Kind: policy.Verify, PR: 9, Branch: "issue/7"},
		{ID: "ver2", Kind: policy.Verify, PR: 12, Branch: "issue/7"},
		{ID: "disc9", Kind: policy.Discuss, Issue: 9},
		{ID: "disc12", Kind: policy.Discuss, Issue: 12},
		{ID: "other", Kind: policy.Implement, Issue: 8, Branch: "issue/8"},
		{ID: "hot", Kind: policy.Verify, PR: 20, Branch: "hotfix"},
		{ID: "hotimpl", Kind: policy.Implement, Branch: "hotfix"},
		{ID: "disc20", Kind: policy.Discuss, Issue: 20},
	}
	in := func(f RetroFlow) string {
		prs := f.PRs(nil, rs)
		var ids string
		for _, r := range rs {
			if f.Has(r, prs) {
				ids += r.ID + " "
			}
		}
		return ids
	}
	issue, pr := NewRetroFlow(7, false, ""), NewRetroFlow(9, true, "issue/7")
	if pr.Issue != 7 || pr.PR != 9 || pr.Target != 9 || pr.String() != "!9 of #7" || issue.String() != "#7" {
		t.Errorf("flows = %+v %q, %+v %q", issue, issue, pr, pr)
	}
	const want = "impl ver ver2 disc9 disc12 "
	if got := in(issue); got != want {
		t.Errorf("flow of #7 = %q, want %q", got, want)
	}
	if got := in(pr); got != want {
		t.Errorf("flow of !9 = %q, want %q", got, want)
	}
	lone := NewRetroFlow(20, true, "hotfix")
	if lone.Issue != 0 || lone.String() != "!20" {
		t.Errorf("lone PR flow = %+v %q", lone, lone)
	}
	if got := in(lone); got != "hot hotimpl disc20 " {
		t.Errorf("flow of !20 = %q", got)
	}
}
