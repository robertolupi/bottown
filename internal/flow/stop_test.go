package flow

import (
	"context"
	"strings"
	"testing"
)

// TestParseStopReadsWhatStopWrites ties ParseStop to the comments
// Supervisor.stop posts, for every reason, with and without a PR.
func TestParseStopReadsWhatStopWrites(t *testing.T) {
	for _, reason := range StopReasons {
		for _, pr := range []int64{0, 12} {
			api := &fakeAPI{}
			_, _ = (&Supervisor{API: api}).stop(context.Background(), Result{Issue: 9, Repo: "demo", PR: pr}, reason, "some detail\n\nPull request: !99 is mentioned here")
			if len(api.comments) != 1 {
				t.Fatalf("%s: comments = %q", reason, api.comments)
			}
			// Its last line carries the reason for other readers.
			if marker := `<!-- bottown:flow-stop v=1 reason="` + string(reason) + `" -->`; !strings.HasSuffix(api.comments[0], "\n\n"+marker) {
				t.Errorf("%s: comment does not end with %s: %q", reason, marker, api.comments[0])
			}
			got, ok := ParseStop(api.comments[0])
			if !ok || got.Reason != reason || got.PR != pr {
				t.Errorf("ParseStop(%q) = %+v, %v; want %s on !%d", api.comments[0], got, ok, reason, pr)
			}
		}
	}
}

// TestParseStopIgnoresOtherComments: the flow's announcements of a round
// share the prefix but are not stops, nor are other comments.
func TestParseStopIgnoresOtherComments(t *testing.T) {
	for _, body := range []string{
		roundBody(string(ChecksFailed), "at abc on !12; starting implement round 1 of 2 to fix them."),
		roundBody("conflicts with main", "on !12 at abc, in a.go; starting implement round 1 of 3 to resolve them."),
		"**bottown flow: something new**\n\ndetail",
		"**bottown run r-1 · implement · claude**\n\nhandover",
		"a comment that quotes **bottown flow: ready to merge**",
		"**bottown flow: ready to merge",
	} {
		if got, ok := ParseStop(body); ok {
			t.Errorf("ParseStop(%q) = %+v, want no stop", body, got)
		}
	}
	// A stop with no detail still parses.
	if got, ok := ParseStop("**bottown flow: run failed**"); !ok || got.Reason != RunFailed {
		t.Errorf("bare stop = %+v, %v", got, ok)
	}
}
