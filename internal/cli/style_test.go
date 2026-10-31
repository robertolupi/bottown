package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

func newInteractiveRenderer(buf *bytes.Buffer) *lipgloss.Renderer {
	r := lipgloss.NewRenderer(buf)
	r.SetColorProfile(termenv.TrueColor)
	return r
}

func TestPrintWhoami(t *testing.T) {
	t.Run("NonInteractiveNoRun", func(t *testing.T) {
		var buf bytes.Buffer
		r := lipgloss.NewRenderer(&buf)
		printWhoami(&buf, r, "agy", "prod", nil, nil, nil)
		got := buf.String()
		want := "actor agy on instance prod\nno active run (read-only; start one with `bottown run start implement|verify N`)\n"
		if got != want {
			t.Fatalf("mismatch:\ngot:  %q\nwant: %q", got, want)
		}
		if strings.Contains(got, "\x1b[") {
			t.Errorf("plain output must not contain ANSI: %q", got)
		}
	})

	t.Run("InteractiveWithRun", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		run := &whoamiRun{
			ID:    "r-ed4df40b",
			Kind:  policy.Implement,
			Repo:  "bottown/bottown",
			Issue: 11,
		}
		printWhoami(&buf, r, "agy", "prod", run, nil, nil)
		got := buf.String()

		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		if !strings.Contains(got, "r-ed4df40b") {
			t.Errorf("expected run ID: %q", got)
		}
		stripped := stripANSI(got)
		wantStripped := "actor agy on instance prod\nrun r-ed4df40b: implement issue #11 in bottown/bottown\n"
		if stripped != wantStripped {
			t.Fatalf("stripped mismatch:\ngot:\n%s\nwant:\n%s", stripped, wantStripped)
		}
	})

	t.Run("InteractiveWithPRRun", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		run := &whoamiRun{
			ID:   "r-12345678",
			Kind: policy.Verify,
			Repo: "bottown/bottown",
			PR:   5,
			SHA:  "abcdef123456",
		}
		printWhoami(&buf, r, "claude", "prod", run, nil, nil)
		got := buf.String()
		stripped := stripANSI(got)
		wantStripped := "actor claude on instance prod\nrun r-12345678: verify PR !5 at abcdef1234 in bottown/bottown\n"
		if stripped != wantStripped {
			t.Fatalf("stripped mismatch:\ngot:\n%s\nwant:\n%s", stripped, wantStripped)
		}
	})

	t.Run("LastRunAndHead", func(t *testing.T) {
		last := &runs.Record{ID: "r-1", Kind: policy.Implement, Repo: "bottown/bottown", Issue: 9, Status: runs.StatusSucceeded}
		head := &headInfo{SHA: "c260e1c", Base: "bottown/main", Behind: 2, Fetched: true}
		want := "actor claude on instance prod\nno active run (read-only; start one with `bottown run start implement|verify N`)\n" +
			"last run r-1: implement issue #9 in bottown/bottown, succeeded\nHEAD c260e1c (detached), 2 commits behind bottown/main\n"
		var plain bytes.Buffer
		printWhoami(&plain, lipgloss.NewRenderer(&plain), "claude", "prod", nil, last, head)
		if plain.String() != want {
			t.Fatalf("plain:\ngot:  %q\nwant: %q", plain.String(), want)
		}
		var buf bytes.Buffer
		printWhoami(&buf, newInteractiveRenderer(&buf), "claude", "prod", nil, last, head)
		if !strings.Contains(buf.String(), "\x1b[") || stripANSI(buf.String()) != want {
			t.Fatalf("interactive: %q", buf.String())
		}
	})
}

func TestPrintRunLs(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	rec := runs.Record{
		ID:       "r-1111",
		Kind:     policy.Implement,
		Issue:    11,
		Actor:    "agy",
		Family:   "google",
		Branch:   "issue/11",
		Status:   runs.StatusSucceeded,
		Started:  now,
		Worktree: "/tmp/nonexistent",
	}
	sr := secrets.Run{
		ID:      "r-2222",
		Kind:    policy.Verify,
		PR:      12,
		Actor:   "claude",
		Family:  "anthropic",
		Branch:  "pr/12",
		SHA:     "1234567890abcdef",
		Status:  runs.StatusFailed,
		Created: now,
		Expires: now.Add(time.Hour),
	}

	t.Run("NonInteractive", func(t *testing.T) {
		var buf bytes.Buffer
		r := lipgloss.NewRenderer(&buf)
		if err := printRunLs(&buf, r, []runs.Record{rec}, []secrets.Run{sr}); err != nil {
			t.Fatal(err)
		}
		got := buf.String()
		if strings.Contains(got, "\x1b[") {
			t.Errorf("plain output must not contain ANSI: %q", got)
		}
		if !strings.Contains(got, "RUN") || !strings.Contains(got, "r-1111") || !strings.Contains(got, "r-2222") {
			t.Fatalf("unexpected output:\n%s", got)
		}
	})

	t.Run("Interactive", func(t *testing.T) {
		recFailed := rec
		recFailed.ID = "r-3333"
		recFailed.Status = runs.StatusFailed

		var plainBuf, intBuf bytes.Buffer
		rPlain := lipgloss.NewRenderer(&plainBuf)
		if err := printRunLs(&plainBuf, rPlain, []runs.Record{rec, recFailed}, []secrets.Run{sr}); err != nil {
			t.Fatal(err)
		}

		rInt := newInteractiveRenderer(&intBuf)
		if err := printRunLs(&intBuf, rInt, []runs.Record{rec, recFailed}, []secrets.Run{sr}); err != nil {
			t.Fatal(err)
		}
		got := intBuf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		if !strings.Contains(got, "succeeded") || !strings.Contains(got, "failed") {
			t.Fatalf("expected statuses in output:\n%s", got)
		}
		stripped := stripANSI(got)
		if stripped != plainBuf.String() {
			t.Fatalf("stripped interactive output does not match plain output:\ngot:\n%s\nwant:\n%s", stripped, plainBuf.String())
		}
	})
}

func TestPrintRunLsAll(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	sr := secrets.Run{
		ID:      "r-2222",
		Repo:    "bottown",
		Kind:    policy.Verify,
		PR:      12,
		Actor:   "claude",
		Family:  "anthropic",
		Status:  runs.StatusFailed,
		Created: now,
		Expires: now.Add(time.Hour),
	}

	var plainBuf, intBuf bytes.Buffer
	rPlain := lipgloss.NewRenderer(&plainBuf)
	if err := printRunLsAll(&plainBuf, rPlain, []secrets.Run{sr}); err != nil {
		t.Fatal(err)
	}

	rInt := newInteractiveRenderer(&intBuf)
	if err := printRunLsAll(&intBuf, rInt, []secrets.Run{sr}); err != nil {
		t.Fatal(err)
	}
	got := intBuf.String()
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("interactive output must contain ANSI: %q", got)
	}
	stripped := stripANSI(got)
	if stripped != plainBuf.String() {
		t.Fatalf("stripped mismatch:\ngot:\n%s\nwant:\n%s", stripped, plainBuf.String())
	}
}

func TestPrintRunShow(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	rec := runs.Record{
		ID:       "r-1111",
		Kind:     policy.Implement,
		Issue:    11,
		Actor:    "agy",
		Family:   "google",
		Status:   runs.StatusSucceeded,
		Worktree: "/tmp/worktree",
		Started:  now,
	}
	sr := secrets.Run{
		ID:      "r-1111",
		Status:  "succeeded",
		Expires: now.Add(2 * time.Hour),
	}

	t.Run("NonInteractive", func(t *testing.T) {
		var buf bytes.Buffer
		r := lipgloss.NewRenderer(&buf)
		printRunShow(&buf, r, &rec, &sr, nil, "/tmp/worktree/.log", "line 1\nline 2\n")
		got := buf.String()
		if strings.Contains(got, "\x1b[") {
			t.Errorf("plain output must not contain ANSI: %q", got)
		}
		if !strings.Contains(got, "local:") || !strings.Contains(got, "server:") || !strings.Contains(got, "log") {
			t.Fatalf("unexpected output:\n%s", got)
		}
	})

	t.Run("Interactive", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		printRunShow(&buf, r, &rec, &sr, nil, "/tmp/worktree/.log", "line 1\nline 2\n")
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		if !strings.Contains(stripped, "local:  r-1111 implement #11 as agy (google), batch, succeeded") {
			t.Errorf("unexpected stripped local line:\n%s", stripped)
		}
	})
}

func TestPrintIssueShowAndList(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local)
	is := server.IssueOut{
		Number: 11,
		Title:  "Colorify CLI",
		State:  "open",
		Author: "agy",
		Labels: []string{"enhancement"},
		Body:   "Some description",
		Comments: []server.CommentOut{
			{ID: 1, Author: "owner", Body: "Looking good", Created: now},
		},
	}
	item := server.IssueItem{
		Number: 11,
		Title:  "Colorify CLI",
		State:  "open",
		Author: "agy",
		Labels: []string{"enhancement"},
	}

	t.Run("IssueShowInteractive", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		printIssueShow(&buf, r, is)
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		if !strings.Contains(stripped, "#11 Colorify CLI [open] by agy (enhancement)") {
			t.Errorf("unexpected stripped header:\n%s", stripped)
		}
	})

	t.Run("IssueListInteractive", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		printIssueList(&buf, r, []server.IssueItem{item})
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		want := "#11   open     agy              Colorify CLI (enhancement)\n"
		if stripped != want {
			t.Fatalf("stripped mismatch:\ngot:  %q\nwant: %q", stripped, want)
		}
	})
}

func TestPrintPRShowAndList(t *testing.T) {
	pr := server.PROut{
		Number:     18,
		Title:      "Diffstat feature",
		State:      "open",
		HeadSHA:    "abcdef123456",
		Author:     "agy",
		HeadBranch: "issue/8",
		BaseBranch: "main",
		Mergeable:  true,
		Body:       "PR description",
		Reviews: []server.ReviewOut{
			{
				Reviewer: "claude",
				State:    "APPROVED",
				CommitID: "abcdef123456",
				Body:     "LGTM",
			},
		},
	}
	item := server.PRItem{
		Number:      18,
		Title:       "Diffstat feature",
		State:       "open",
		HeadSHA:     "abcdef123456",
		Author:      "agy",
		ReviewState: "APPROVED",
	}

	t.Run("PRShowInteractive", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		printPRShow(&buf, r, pr, false, false, false)
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		if !strings.Contains(stripped, "!18 Diffstat feature [open] by agy") {
			t.Errorf("unexpected stripped header:\n%s", stripped)
		}
		if !strings.Contains(stripped, "--- review APPROVED by claude at abcdef1234") {
			t.Errorf("unexpected stripped review:\n%s", stripped)
		}
	})

	t.Run("PRListInteractive", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		printPRList(&buf, r, []server.PRItem{item})
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		want := "!18   open     abcdef12   agy              APPROVED           Diffstat feature\n"
		if stripped != want {
			t.Fatalf("stripped mismatch:\ngot:  %q\nwant: %q", stripped, want)
		}
	})
}

func TestPrintActorAndRepo(t *testing.T) {
	t.Run("ActorList", func(t *testing.T) {
		wts := []actorWorktree{
			{Actor: "agy", Run: "r-1111", Head: "issue/11", Path: "/path/to/agy"},
			{Actor: "claude", Run: "", Head: "main", Path: "/path/to/claude"},
		}

		var plainBuf, intBuf bytes.Buffer
		rPlain := lipgloss.NewRenderer(&plainBuf)
		if err := printActorList(&plainBuf, rPlain, wts); err != nil {
			t.Fatal(err)
		}

		rInt := newInteractiveRenderer(&intBuf)
		if err := printActorList(&intBuf, rInt, wts); err != nil {
			t.Fatal(err)
		}
		got := intBuf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		if stripped != plainBuf.String() {
			t.Fatalf("stripped mismatch:\ngot:\n%s\nwant:\n%s", stripped, plainBuf.String())
		}
	})

	t.Run("ActorSetup", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		printActorSetup(&buf, r, "agy", "google", "agy", "prod", []string{"origin"})
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		want := "agy (google, agy): identity agy <agy@bottown.local>, instance prod, pushes blocked to origin\n"
		if stripped != want {
			t.Fatalf("stripped mismatch:\ngot:  %q\nwant: %q", stripped, want)
		}
	})

	t.Run("RepoAdd", func(t *testing.T) {
		var buf bytes.Buffer
		r := newInteractiveRenderer(&buf)
		printRepoRemoteAdded(&buf, r, "bottown", "http://127.0.0.1:3031/git/bottown/bottown.git")
		printRepoAdded(&buf, r, "bottown", "prod", "http://127.0.0.1:3030/bottown/bottown")
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		want := "added remote bottown -> http://127.0.0.1:3031/git/bottown/bottown.git\nbottown is hosted on bottown prod: http://127.0.0.1:3030/bottown/bottown\n"
		if stripped != want {
			t.Fatalf("stripped mismatch:\ngot:  %q\nwant: %q", stripped, want)
		}
	})
}
