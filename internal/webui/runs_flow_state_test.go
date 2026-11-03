package webui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/secrets"
)

// TestComputeHistoryRuns_FlowState tests that ComputeHistoryRuns derives
// authoritative flow-level states (#155): ready to merge, needs changes,
// in progress, needs intervention, needs review, merged, and unknown.
func TestComputeHistoryRuns_FlowState(t *testing.T) {
	now := time.Now().UTC()
	h1 := "commit-h1"
	h2 := "commit-h2"

	actors := []ActorDetail{
		{Name: "claude", Family: "anthropic", Harness: "claude"},
		{Name: "kimi", Family: "moonshot", Harness: "kimi"},
		{Name: "agy", Family: "google", Harness: "agy"},
	}

	ended1 := now.Add(-30 * time.Minute)
	ended2 := now.Add(-20 * time.Minute)
	ended3 := now.Add(-10 * time.Minute)
	ended4 := now.Add(-5 * time.Minute)

	runs := []secrets.Run{
		// PR 10: Ready to merge (approved at current head h2 by cross-family verifier kimi)
		{
			ID: "r-ready-imp", Kind: "implement", Actor: "claude", Family: "anthropic",
			Repo: "bottown", Issue: 10, PR: 10, Status: "succeeded",
			Created: now.Add(-40 * time.Minute), Ended: &ended1,
		},
		{
			ID: "r-ready-ver", Kind: "verify", Actor: "kimi", Family: "moonshot",
			Repo: "bottown", Issue: 10, PR: 10, Status: "succeeded", Outcome: "approved",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// PR 20: Needs changes (changes requested at current head)
		{
			ID: "r-changes-ver", Kind: "verify", Actor: "kimi", Family: "moonshot",
			Repo: "bottown", Issue: 20, PR: 20, Status: "succeeded", Outcome: "changes_requested",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// PR 30: Stale review -> Needs review (approval was at old commit h1, current head is h2)
		{
			ID: "r-stale-ver", Kind: "verify", Actor: "kimi", Family: "moonshot",
			Repo: "bottown", Issue: 30, PR: 30, Status: "succeeded", Outcome: "approved",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// PR 40: Conflicts with main -> Needs intervention
		{
			ID: "r-conflict-ver", Kind: "verify", Actor: "kimi", Family: "moonshot",
			Repo: "bottown", Issue: 40, PR: 40, Status: "succeeded", Outcome: "approved",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// PR 50: Merged -> Merged
		{
			ID: "r-merged-imp", Kind: "implement", Actor: "claude", Family: "anthropic",
			Repo: "bottown", Issue: 50, PR: 50, Status: "succeeded", Outcome: "pr_opened",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// Issue 60: Run failed without PR -> Needs intervention
		{
			ID: "r-failed-imp", Kind: "implement", Actor: "claude", Family: "anthropic",
			Repo: "bottown", Issue: 60, Status: "failed",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// Issue 65: Succeeded run without PR -> Unknown
		{
			ID: "r-nopr-imp", Kind: "implement", Actor: "claude", Family: "anthropic",
			Repo: "bottown", Issue: 65, Status: "succeeded",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// Issue 70: Active flow -> In progress
		{
			ID: "r-active-imp", Kind: "implement", Actor: "claude", Family: "anthropic",
			Repo: "bottown", Issue: 70, PR: 70, Status: "succeeded", Outcome: "pr_opened",
			Created: now.Add(-25 * time.Minute), Ended: &ended2,
		},

		// Issue 80: Discuss run (not a flow)
		{
			ID: "r-discuss", Kind: "discuss", Actor: "claude", Family: "anthropic",
			Repo: "bottown", Issue: 80, Status: "succeeded", Outcome: "commented",
			Created: now.Add(-25 * time.Minute), Ended: &ended3,
		},
	}

	prs := []*sdk.PullRequest{
		{
			Index: 10, State: sdk.StateOpen, Mergeable: true,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/10", Sha: h2},
		},
		{
			Index: 20, State: sdk.StateOpen, Mergeable: true,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/20", Sha: h2},
		},
		{
			Index: 30, State: sdk.StateOpen, Mergeable: true,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/30", Sha: h2},
		},
		{
			Index: 40, State: sdk.StateOpen, Mergeable: false,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/40", Sha: h2},
		},
		{
			Index: 50, State: sdk.StateClosed, HasMerged: true,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/50", Sha: h2},
		},
		{
			Index: 70, State: sdk.StateOpen, Mergeable: true,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/70", Sha: h2},
		},
	}

	reviews := map[int64][]*sdk.PullReview{
		// PR 10: Approved at h2 by kimi (cross-family)
		10: {
			{
				ID: 1, Reviewer: &sdk.User{UserName: "kimi"}, State: sdk.ReviewStateApproved,
				CommitID: h2, Official: true, Stale: false, Dismissed: false, Submitted: ended2,
			},
		},
		// PR 20: Changes requested at h2 by kimi
		20: {
			{
				ID: 2, Reviewer: &sdk.User{UserName: "kimi"}, State: sdk.ReviewStateRequestChanges,
				CommitID: h2, Official: true, Stale: false, Dismissed: false, Submitted: ended2,
			},
		},
		// PR 30: Stale approval at h1 (current head is h2)
		30: {
			{
				ID: 3, Reviewer: &sdk.User{UserName: "kimi"}, State: sdk.ReviewStateApproved,
				CommitID: h1, Official: true, Stale: true, Dismissed: false, Submitted: ended4,
			},
		},
		// PR 40: Approved at h2, but Mergeable is false (conflicts)
		40: {
			{
				ID: 4, Reviewer: &sdk.User{UserName: "kimi"}, State: sdk.ReviewStateApproved,
				CommitID: h2, Official: true, Stale: false, Dismissed: false, Submitted: ended2,
			},
		},
	}

	issues := []*sdk.Issue{
		{Index: 10, Title: "Issue 10"},
		{Index: 20, Title: "Issue 20"},
		{Index: 30, Title: "Issue 30"},
		{Index: 40, Title: "Issue 40"},
		{Index: 50, Title: "Issue 50"},
		{Index: 60, Title: "Issue 60"},
		{Index: 65, Title: "Issue 65"},
		{Index: 70, Title: "Issue 70"},
		{Index: 80, Title: "Issue 80"},
	}

	fix := &runsFixture{
		actors:   actors,
		runs:     runs,
		prs:      prs,
		reviews:  reviews,
		issues:   issues,
		comments: make(map[int64][]*sdk.Comment),
	}

	_, port := newRunsFixtureServer(t, fix)
	top := t.TempDir()

	// Active flow on issue 70
	if err := os.MkdirAll(filepath.Join(top, ".bottown"), 0o700); err != nil {
		t.Fatal(err)
	}
	flowRec := fmt.Sprintf(`{"issue":70,"implement":"claude","verify":"kimi","rounds":3,"pid":%d}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(top, ".bottown", "flow-70.json"), []byte(flowRec), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := NewServer(Config{
		Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
		RepoTop:  top,
		FullRepo: "bottown/bottown",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	history, err := s.ComputeHistoryRuns(ctx, HistoryFilter{})
	if err != nil {
		t.Fatal(err)
	}

	byID := make(map[string]HistoryRunInfo, len(history))
	for _, h := range history {
		byID[h.ID] = h
	}

	// 1. Ready to merge
	h10 := byID["r-ready-ver"]
	if h10.FlowState != "ready_to_merge" {
		t.Fatalf("r-ready-ver: flow_state = %q, want ready_to_merge", h10.FlowState)
	}
	if h10.ReviewDecision != string(review.Approved) || !h10.ReviewAtHead {
		t.Fatalf("r-ready-ver: review_decision = %q, at_head = %v, want approved at head", h10.ReviewDecision, h10.ReviewAtHead)
	}

	// 2. Needs changes
	h20 := byID["r-changes-ver"]
	if h20.FlowState != "needs_changes" {
		t.Fatalf("r-changes-ver: flow_state = %q, want needs_changes", h20.FlowState)
	}
	if h20.ReviewDecision != string(review.ChangesRequested) {
		t.Fatalf("r-changes-ver: review_decision = %q, want changes requested", h20.ReviewDecision)
	}

	// 3. Stale review: must NOT imply readiness!
	h30 := byID["r-stale-ver"]
	if h30.FlowState != "needs_review" {
		t.Fatalf("r-stale-ver: flow_state = %q, want needs_review", h30.FlowState)
	}
	if h30.ReviewDecision != string(review.NeedsReview) || h30.ReviewAtHead {
		t.Fatalf("r-stale-ver: review_decision = %q, at_head = %v, want needs review not at head", h30.ReviewDecision, h30.ReviewAtHead)
	}

	// 4. Conflicts with main -> Needs intervention
	h40 := byID["r-conflict-ver"]
	if h40.FlowState != "needs_intervention" {
		t.Fatalf("r-conflict-ver: flow_state = %q, want needs_intervention", h40.FlowState)
	}
	if h40.PRMergeable == nil || *h40.PRMergeable {
		t.Fatalf("r-conflict-ver: pr_mergeable = %v, want false", h40.PRMergeable)
	}

	// 5. Merged PR
	h50 := byID["r-merged-imp"]
	if h50.FlowState != "merged" || !h50.PRMerged {
		t.Fatalf("r-merged-imp: flow_state = %q, pr_merged = %v, want merged", h50.FlowState, h50.PRMerged)
	}

	// 6. Failed run without PR -> Needs intervention
	h60 := byID["r-failed-imp"]
	if h60.FlowState != "needs_intervention" {
		t.Fatalf("r-failed-imp: flow_state = %q, want needs_intervention", h60.FlowState)
	}

	// 6b. Succeeded run without PR -> Unknown (never guessed or inferred from outcome)
	h65 := byID["r-nopr-imp"]
	if h65.FlowState != "unknown" {
		t.Fatalf("r-nopr-imp: flow_state = %q, want unknown", h65.FlowState)
	}

	// 7. Active flow -> in_progress
	h70 := byID["r-active-imp"]
	if h70.FlowState != "in_progress" {
		t.Fatalf("r-active-imp: flow_state = %q, want in_progress", h70.FlowState)
	}

	// 8. Discuss run (not a flow) -> empty flow_state
	h80 := byID["r-discuss"]
	if h80.FlowState != "" {
		t.Fatalf("r-discuss: flow_state = %q, want empty for discuss", h80.FlowState)
	}
}

// TestComputeHistoryRuns_ReadFailures tests that missing or read-failed PR and review data
// leaves the flow-level state explicitly unknown/neutral, rather than Needs intervention or Ready.
func TestComputeHistoryRuns_ReadFailures(t *testing.T) {
	now := time.Now().UTC()
	h := "commit-head"
	ended := now.Add(-10 * time.Minute)

	actors := []ActorDetail{
		{Name: "claude", Family: "anthropic", Harness: "claude"},
		{Name: "kimi", Family: "moonshot", Harness: "kimi"},
	}

	runs := []secrets.Run{
		// Implement run on issue 10 (succeeded, pr_opened)
		{
			ID: "r-imp", Kind: "implement", Actor: "claude", Family: "anthropic",
			Repo: "bottown", Issue: 10, Branch: "issue/10", Status: "succeeded", Outcome: "pr_opened",
			Created: now.Add(-30 * time.Minute), Ended: &ended,
			Digest: secrets.Digest{Summary: "Opened PR !11"},
		},
		// Verify run on PR 11 (succeeded, approved)
		{
			ID: "r-ver", Kind: "verify", Actor: "kimi", Family: "moonshot",
			Repo: "bottown", Issue: 10, PR: 11, Status: "succeeded", Outcome: "approved",
			Created: now.Add(-20 * time.Minute), Ended: &ended,
		},
	}

	prs := []*sdk.PullRequest{
		{
			Index: 11, State: sdk.StateOpen, Mergeable: true,
			Poster: &sdk.User{UserName: "claude"}, Head: &sdk.PRBranchInfo{Ref: "issue/10", Sha: h},
		},
	}

	reviews := map[int64][]*sdk.PullReview{
		11: {
			{
				ID: 1, Reviewer: &sdk.User{UserName: "kimi"}, State: sdk.ReviewStateApproved,
				CommitID: h, Official: true, Stale: false, Dismissed: false, Submitted: ended,
			},
		},
	}

	issues := []*sdk.Issue{
		{Index: 10, Title: "Issue 10"},
	}

	t.Run("pulls read failure (500) results in unknown", func(t *testing.T) {
		fix := &runsFixture{
			actors:   actors,
			runs:     runs,
			prs:      prs,
			reviews:  reviews,
			issues:   issues,
			pullsErr: 500,
		}

		_, port := newRunsFixtureServer(t, fix)
		top := t.TempDir()

		s, err := NewServer(Config{
			Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
			RepoTop:  top,
			FullRepo: "bottown/bottown",
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		ctx := context.Background()
		history, err := s.ComputeHistoryRuns(ctx, HistoryFilter{})
		if err != nil {
			t.Fatal(err)
		}

		byID := make(map[string]HistoryRunInfo, len(history))
		for _, item := range history {
			byID[item.ID] = item
		}

		// When pulls read fails, implement run loses its resolved PR.
		// It must NOT show needs_intervention; it must remain explicitly unknown!
		hImp := byID["r-imp"]
		if hImp.FlowState != "unknown" {
			t.Fatalf("r-imp with pulls 500: flow_state = %q, want unknown", hImp.FlowState)
		}

		// Verify run has explicit PR 11 in run record, but PR cannot be read.
		// It must also remain explicitly unknown!
		hVer := byID["r-ver"]
		if hVer.FlowState != "unknown" {
			t.Fatalf("r-ver with pulls 500: flow_state = %q, want unknown", hVer.FlowState)
		}
	})

	t.Run("reviews read failure (500) results in unknown", func(t *testing.T) {
		fix := &runsFixture{
			actors:     actors,
			runs:       runs,
			prs:        prs,
			reviews:    reviews,
			issues:     issues,
			reviewsErr: map[int64]int{11: 500},
		}

		_, port := newRunsFixtureServer(t, fix)
		top := t.TempDir()

		s, err := NewServer(Config{
			Instance: config.Instance{Name: "prod", ServerPort: port, OwnerLogin: "rlupi"},
			RepoTop:  top,
			FullRepo: "bottown/bottown",
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		ctx := context.Background()
		history, err := s.ComputeHistoryRuns(ctx, HistoryFilter{})
		if err != nil {
			t.Fatal(err)
		}

		byID := make(map[string]HistoryRunInfo, len(history))
		for _, item := range history {
			byID[item.ID] = item
		}

		// PR is open and mergeable, but reviews read failed (500).
		// Must remain explicitly unknown!
		hVer := byID["r-ver"]
		if hVer.FlowState != "unknown" {
			t.Fatalf("r-ver with reviews 500: flow_state = %q, want unknown", hVer.FlowState)
		}
	})
}
