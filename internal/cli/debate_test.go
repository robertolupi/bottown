package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

func TestDebateDate(t *testing.T) {
	now := time.Date(2026, 11, 7, 23, 0, 0, 0, time.Local)
	for in, want := range map[string]string{"": "", "today": "2026-11-07", "20261031": "2026-10-31"} {
		if got, err := debateDate(in, now); err != nil || got != want {
			t.Errorf("debateDate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"2026-10-31", "20261340", "yesterday"} {
		if _, err := debateDate(bad, now); err == nil {
			t.Errorf("debateDate(%q) accepted", bad)
		}
	}
}

func TestDebateActors(t *testing.T) {
	if err := debateActors([]string{"claude", "codex:gpt-x", "agy"}); err != nil {
		t.Error(err)
	}
	if err := debateActors([]string{"claude", "claude:opus"}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("an actor twice: %v", err)
	}
}

func TestDebatePrompt(t *testing.T) {
	files := []server.WikiFile{
		{Path: "Home.md"}, {Path: "Review-7-t.md"},
		{Path: "reviews/7-t/r1-claude.md"}, {Path: "reviews/7-t/r1-codex.md"}, {Path: "reviews/7-t/r1-consolidated.md"},
		{Path: "reviews/7-t/r2-codex.md"}, {Path: "reviews/7-t/r2-claude.md"},
		{Path: "reviews/8-u/r1-consolidated.md"},
	}
	run := secrets.Run{Kind: policy.Discuss, Actor: "claude", Debate: &secrets.Debate{Name: "7-t", Round: 2, Question: "Q?"}}
	got := debatePrompt(run, files)
	want := &runs.DebatePrompt{Round: 2, Question: "Q?", File: "reviews/7-t/r2-claude.md", Page: "Review-7-t",
		Earlier: []string{"reviews/7-t/r1-consolidated.md"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discuss: %+v, want %+v", got, want)
	}
	run.Debate.Raw = true
	if got := debatePrompt(run, files).Earlier; len(got) != 3 {
		t.Errorf("raw discuss reads %v", got)
	}
	run = secrets.Run{Kind: policy.Consolidate, Actor: "agy", Debate: &secrets.Debate{Name: "7-t", Round: 2}}
	got = debatePrompt(run, files)
	want = &runs.DebatePrompt{Round: 2, File: "reviews/7-t/r2-consolidated.md", Page: "Review-7-t",
		Answers: []string{"reviews/7-t/r2-claude.md", "reviews/7-t/r2-codex.md"}, Earlier: []string{"reviews/7-t/r1-consolidated.md"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("consolidate: %+v, want %+v", got, want)
	}
}

func TestConsolidatedText(t *testing.T) {
	rec := runs.Record{ID: "r-1", Kind: policy.Consolidate, Actor: "agy", Family: "google"}
	run := secrets.Run{Org: "bottown", Repo: "demo", Debate: &secrets.Debate{Name: "7-t", Round: 2}}
	got := consolidatedText(rec, run, runs.StatusSucceeded, 0)
	if !strings.HasPrefix(got, "**bottown run `r-1`** · consolidate · agy (google)") ||
		!strings.Contains(got, "Round 2 of the debate is consolidated: [Review-7-t](/bottown/demo/wiki/Review-7-t).") {
		t.Errorf("consolidated text:\n%s", got)
	}
	if got := consolidatedText(rec, run, runs.StatusFailed, 1); !strings.Contains(got, "not consolidated by this run") {
		t.Errorf("failed consolidation text:\n%s", got)
	}
}
