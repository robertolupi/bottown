package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// interactiveOut is a writer that keeps ANSI, as a terminal does: a
// colorprofile.Writer with the TrueColor profile.
func interactiveOut(buf *bytes.Buffer) io.Writer {
	return &colorprofile.Writer{Forward: buf, Profile: colorprofile.TrueColor}
}

func TestPrintWhoami(t *testing.T) {
	t.Run("NonInteractiveNoRun", func(t *testing.T) {
		var buf bytes.Buffer
		r := colorOut(&buf)
		printWhoami(r, "agy", "prod", "bottown", nil, nil, nil)
		got := buf.String()
		want := "actor agy on instance prod\nforge remote bottown\nno active run (read-only; start one with `bottown run start implement|verify N`)\n"
		if got != want {
			t.Fatalf("mismatch:\ngot:  %q\nwant: %q", got, want)
		}
		if strings.Contains(got, "\x1b[") {
			t.Errorf("plain output must not contain ANSI: %q", got)
		}
	})

	t.Run("InteractiveWithRun", func(t *testing.T) {
		var buf bytes.Buffer
		r := interactiveOut(&buf)
		run := &whoamiRun{
			ID:    "r-ed4df40b",
			Kind:  policy.Implement,
			Repo:  "bottown/bottown",
			Issue: 11,
		}
		printWhoami(r, "agy", "prod", "bottown", run, nil, nil)
		got := buf.String()

		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		if !strings.Contains(got, "r-ed4df40b") {
			t.Errorf("expected run ID: %q", got)
		}
		stripped := stripANSI(got)
		wantStripped := "actor agy on instance prod\nforge remote bottown\nrun r-ed4df40b: implement issue #11 in bottown/bottown\n"
		if stripped != wantStripped {
			t.Fatalf("stripped mismatch:\ngot:\n%s\nwant:\n%s", stripped, wantStripped)
		}
	})

	t.Run("InteractiveWithPRRun", func(t *testing.T) {
		var buf bytes.Buffer
		r := interactiveOut(&buf)
		run := &whoamiRun{
			ID:   "r-12345678",
			Kind: policy.Verify,
			Repo: "bottown/bottown",
			PR:   5,
			SHA:  "abcdef123456",
		}
		printWhoami(r, "claude", "prod", "bottown", run, nil, nil)
		got := buf.String()
		stripped := stripANSI(got)
		wantStripped := "actor claude on instance prod\nforge remote bottown\nrun r-12345678: verify PR !5 at abcdef1234 in bottown/bottown\n"
		if stripped != wantStripped {
			t.Fatalf("stripped mismatch:\ngot:\n%s\nwant:\n%s", stripped, wantStripped)
		}
	})

	t.Run("LastRunAndHead", func(t *testing.T) {
		last := &runs.Record{ID: "r-1", Kind: policy.Implement, Repo: "bottown/bottown", Issue: 9, Status: runs.StatusSucceeded}
		head := &headInfo{SHA: "c260e1c", Base: "bottown/main", Behind: 2, Fetched: true}
		want := "actor claude on instance prod\nforge remote bottown\nno active run (read-only; start one with `bottown run start implement|verify N`)\n" +
			"last run r-1: implement issue #9 in bottown/bottown, succeeded\nHEAD c260e1c (detached), 2 commits behind bottown/main\n"
		var plain bytes.Buffer
		printWhoami(colorOut(&plain), "claude", "prod", "bottown", nil, last, head)
		if plain.String() != want {
			t.Fatalf("plain:\ngot:  %q\nwant: %q", plain.String(), want)
		}
		var buf bytes.Buffer
		printWhoami(interactiveOut(&buf), "claude", "prod", "bottown", nil, last, head)
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
		r := colorOut(&buf)
		if err := printRunLs(r, []runs.Record{rec}, []secrets.Run{sr}); err != nil {
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
		rPlain := colorOut(&plainBuf)
		if err := printRunLs(rPlain, []runs.Record{rec, recFailed}, []secrets.Run{sr}); err != nil {
			t.Fatal(err)
		}

		rInt := interactiveOut(&intBuf)
		if err := printRunLs(rInt, []runs.Record{rec, recFailed}, []secrets.Run{sr}); err != nil {
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

func TestPrintRunLsFiltering(t *testing.T) {
	now := time.Now()
	endedTime := now.Add(-10 * time.Minute)

	// 1. Local ended run whose server record is ended:
	// must keep KEY "ended", not "gone".
	recLocalEnded := runs.Record{
		ID:       "r-local",
		Kind:     policy.Implement,
		Issue:    1,
		Actor:    "claude",
		Family:   "anthropic",
		Branch:   "issue/1",
		Status:   runs.StatusFailed,
		Started:  now.Add(-time.Hour),
		Ended:    &endedTime,
		Worktree: "/tmp/nonexistent",
	}
	srLocalEnded := secrets.Run{
		ID:      "r-local",
		Kind:    policy.Implement,
		Issue:   1,
		Actor:   "claude",
		Family:  "anthropic",
		Branch:  "issue/1",
		Status:  runs.StatusFailed,
		Created: now.Add(-time.Hour),
		Expires: now.Add(time.Hour),
		Ended:   &endedTime,
	}

	// 2. Server-only expired run (never ended, sr.Ended == nil):
	// must remain listed with STATUS "dead" and KEY "expired".
	srExpiredStuck := secrets.Run{
		ID:      "r-stuck",
		Kind:    policy.Implement,
		Issue:   2,
		Actor:   "codex",
		Family:  "openai",
		Branch:  "issue/2",
		Created: now.Add(-2 * time.Hour),
		Expires: now.Add(-time.Hour), // expired, never ended
	}

	// 3. Server-only ended run (sr.Ended != nil):
	// must be hidden from default printRunLs.
	srServerEnded := secrets.Run{
		ID:      "r-ended",
		Kind:    policy.Verify,
		PR:      3,
		Actor:   "agy",
		Family:  "google",
		Branch:  "issue/3",
		Status:  runs.StatusSucceeded,
		Created: now.Add(-time.Hour),
		Expires: now.Add(time.Hour),
		Ended:   &endedTime,
	}

	var buf bytes.Buffer
	r := colorOut(&buf)
	local := []runs.Record{recLocalEnded}
	remote := []secrets.Run{srLocalEnded, srExpiredStuck, srServerEnded}

	if err := printRunLs(r, local, remote); err != nil {
		t.Fatal(err)
	}
	got := buf.String()

	// 1. Local ended run must show "ended", not "gone"
	if !strings.Contains(got, "r-local") {
		t.Errorf("expected r-local in output:\n%s", got)
	}
	if strings.Contains(got, "gone") {
		t.Errorf("r-local must not show KEY 'gone':\n%s", got)
	}
	if !strings.Contains(got, "ended") {
		t.Errorf("r-local must show KEY 'ended':\n%s", got)
	}

	// 2. Server-only expired run must be listed with dead/expired
	if !strings.Contains(got, "r-stuck") {
		t.Errorf("expected r-stuck (expired never-ended) in output:\n%s", got)
	}
	if !strings.Contains(got, "dead") {
		t.Errorf("r-stuck must show STATUS 'dead':\n%s", got)
	}
	if !strings.Contains(got, "expired") {
		t.Errorf("r-stuck must show KEY 'expired':\n%s", got)
	}

	// 3. Server-only ended run must be hidden
	if strings.Contains(got, "r-ended") {
		t.Errorf("r-ended (server-only ended) must be hidden from output:\n%s", got)
	}
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
	rPlain := colorOut(&plainBuf)
	if err := printRunLsAll(rPlain, []secrets.Run{sr}); err != nil {
		t.Fatal(err)
	}

	rInt := interactiveOut(&intBuf)
	if err := printRunLsAll(rInt, []secrets.Run{sr}); err != nil {
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
		r := colorOut(&buf)
		printRunShow(r, &rec, &sr, nil, "/tmp/worktree/.log", "line 1\nline 2\n")
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
		r := interactiveOut(&buf)
		printRunShow(r, &rec, &sr, nil, "/tmp/worktree/.log", "line 1\nline 2\n")
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		if !strings.Contains(stripped, "local:  r-1111 implement #11 as agy (google), batch, succeeded") {
			t.Errorf("unexpected stripped local line:\n%s", stripped)
		}
	})

	t.Run("PurgedDigest", func(t *testing.T) {
		var buf bytes.Buffer
		in, turns := int64(15775), int64(1)
		d := sr
		d.Purged = true
		d.Digest = secrets.Digest{Summary: "Opened PR !4.\nDetails.", Usage: &secrets.Usage{InputTokens: &in, Turns: &turns}}
		printRunShow(colorOut(&buf), nil, &d, nil, "", "")
		got := buf.String()
		for _, want := range []string{", purged (digest kept)\n", "usage 15775 in tokens · 1 turns\n", "summary Opened PR !4.\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("lacks %q:\n%s", want, got)
			}
		}
	})
}

func TestFormatUsage(t *testing.T) {
	in, out, cr, cw, turns, ms := int64(1174), int64(20), int64(11383), int64(5834), int64(1), int64(2752)
	cost := 0.0491618
	full := secrets.Usage{InputTokens: &in, OutputTokens: &out, CacheReadTokens: &cr, CacheWriteTokens: &cw, CostUSD: &cost, Turns: &turns, DurationMS: &ms}
	if got := formatUsage(full); got != "1174 in, 20 out, 11383 cache read, 5834 cache write tokens · $0.0492 · 1 turns · 2.752s" {
		t.Errorf("full: %q", got)
	}
	if got := formatUsage(secrets.Usage{}); got != "none reported" {
		t.Errorf("empty: %q", got)
	}
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
		r := interactiveOut(&buf)
		atts := []runs.Attachment{
			{ID: 3, Name: "shot.png", Size: 2048, ContentType: "image/png", Path: "/run/attachments/3-shot.png"},
			{ID: 4, Name: "log.txt", Size: 10, Comment: 9, Error: "download failed: 404 Not Found"},
		}
		printIssueShow(r, is, atts)
		got := buf.String()
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("interactive output must contain ANSI: %q", got)
		}
		stripped := stripANSI(got)
		if !strings.Contains(stripped, "#11 Colorify CLI [open] by agy (enhancement)") {
			t.Errorf("unexpected stripped header:\n%s", stripped)
		}
		for _, want := range []string{
			"--- attachments (2)\n",
			"- `/run/attachments/3-shot.png`: \"shot.png\" (image/png, 2.0 KiB, on the description)\n",
			"- \"log.txt\" (10 bytes, on comment 9): download failed: 404 Not Found\n",
		} {
			if !strings.Contains(stripped, want) {
				t.Errorf("issue show lacks %q:\n%s", want, stripped)
			}
		}
	})

	t.Run("IssueListInteractive", func(t *testing.T) {
		var buf bytes.Buffer
		r := interactiveOut(&buf)
		printIssueList(r, []server.IssueItem{item})
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
		r := interactiveOut(&buf)
		printPRShow(r, pr, false, false, false)
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
		r := interactiveOut(&buf)
		printPRList(r, []server.PRItem{item})
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
		rPlain := colorOut(&plainBuf)
		if err := printActorList(rPlain, wts); err != nil {
			t.Fatal(err)
		}

		rInt := interactiveOut(&intBuf)
		if err := printActorList(rInt, wts); err != nil {
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
		r := interactiveOut(&buf)
		printActorSetup(r, "agy", "google", "agy", "prod", []string{"origin"})
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
		r := interactiveOut(&buf)
		printRepoRemoteAdded(r, "bottown", "http://127.0.0.1:3031/git/bottown/bottown.git")
		printRepoAdded(r, "bottown", "prod", "http://127.0.0.1:3030/bottown/bottown")
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
