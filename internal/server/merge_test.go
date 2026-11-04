package server

import (
	"maps"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

func TestLiveRunOnPR(t *testing.T) {
	now := time.Now()
	ended := now.Add(-time.Minute)
	all := map[string]secrets.Run{
		"r-ended":      {ID: "r-ended", Org: "bottown", Repo: "bottown", PR: 15, Ended: &ended},
		"r-expired":    {ID: "r-expired", Org: "bottown", Repo: "bottown", PR: 15, Expires: now.Add(-time.Second)},
		"r-other-repo": {ID: "r-other-repo", Org: "bottown", Repo: "other", PR: 15},
		"r-other-pr":   {ID: "r-other-pr", Org: "bottown", Repo: "bottown", PR: 16, Issue: 16, Branch: "issue/16"},
	}
	if run, ok := liveRunOnPR(all, "bottown", "bottown", 15, "issue/15", now); ok {
		t.Fatalf("found %s, want no live run on !15", run.ID)
	}
	for _, live := range []secrets.Run{
		{ID: "r-verify", Kind: "verify", PR: 15},
		{ID: "r-implement", Kind: "implement", Issue: 15, Branch: "issue/15"},
		{ID: "r-discuss", Kind: "discuss", Issue: 15},
	} {
		live.Org, live.Repo, live.Expires = "Bottown", "bottown", now.Add(time.Hour)
		runs := maps.Clone(all)
		runs[live.ID] = live
		run, ok := liveRunOnPR(runs, "bottown", "bottown", 15, "issue/15", now)
		if !ok || run.ID != live.ID {
			t.Errorf("%s: got %q, %v; want it found", live.ID, run.ID, ok)
		}
	}
}
