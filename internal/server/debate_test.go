package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/wiki"
)

func debater(id, actor, kind string, round int) secrets.Principal {
	return secrets.Principal{Actor: actor, Run: &secrets.Run{
		ID: id, Kind: kind, Actor: actor, Org: "bottown", Repo: "demo", Issue: 315, Spec: "m-" + actor, SHA: "abc123",
		Debate: &secrets.Debate{Name: "315-x", Round: round},
	}}
}

// TestDebateRoundFiles: a round's discuss runs write only their own answer,
// once, and cannot read each other's; the consolidate run reads them and
// writes the consolidation, and the store rewrites the debate's page in
// the same commit. Others read a round once it is consolidated.
func TestDebateRoundFiles(t *testing.T) {
	w := newWikiRepo(t, true)
	ctx := context.Background()
	c1, x1 := debater("r-c1", "claude", "discuss", 1), debater("r-x1", "codex", "discuss", 1)
	put := func(p secrets.Principal, file, content, base string) (WikiPutOut, error) {
		return w.s.opWikiPut(ctx, p, WikiPutIn{Repo: "demo", Path: file, Content: content, Base: base})
	}
	read := func(p secrets.Principal, file string) error {
		_, err := w.s.opWikiRead(ctx, p, "demo", file)
		return err
	}
	if _, err := put(c1, "reviews/315-x/r1-claude.md", "---\nactor: mallory\n---\n\n# Claude's answer\n", ""); err != nil {
		t.Fatal(err)
	}
	got := w.read("reviews/315-x/r1-claude.md").Content
	if !strings.HasPrefix(got, "---\nround: 1\nactor: claude\nmodel: m-claude\nrun: r-c1\nissue: 315\nsha: abc123\ndate: ") ||
		!strings.HasSuffix(got, "---\n\n# Claude's answer\n") || strings.Contains(got, "mallory") {
		t.Fatalf("stamped answer:\n%s", got)
	}
	sha := w.read("reviews/315-x/r1-claude.md").SHA
	if _, err := put(c1, "reviews/315-x/r1-claude.md", "again\n", sha); status(err) != http.StatusConflict || !strings.Contains(err.Error(), "written once") {
		t.Fatalf("second write of an answer: %v", err)
	}
	if _, err := put(c1, "reviews/315-x/r1-codex.md", "forged\n", ""); status(err) != http.StatusForbidden {
		t.Fatalf("write of another actor's answer: %v", err)
	}
	if _, err := w.s.opWikiRm(ctx, c1, WikiRmIn{Repo: "demo", Path: "reviews/315-x/r1-claude.md", Base: sha}); status(err) != http.StatusForbidden {
		t.Fatalf("delete of an answer: %v", err)
	}
	if _, err := put(x1, "reviews/315-x/r1-codex.md", "# Codex's answer\n", ""); err != nil {
		t.Fatal(err)
	}
	// Independence: neither reads the other's answer, nor lists it.
	if err := read(x1, "reviews/315-x/r1-claude.md"); status(err) != http.StatusForbidden {
		t.Fatalf("codex reads claude's answer: %v", err)
	}
	if err := read(x1, "reviews/315-x/r1-codex.md"); err != nil {
		t.Fatalf("codex reads its own answer: %v", err)
	}
	ls, err := w.s.opWikiList(ctx, x1, "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range ls.Files {
		if f.Path == "reviews/315-x/r1-claude.md" {
			t.Fatal("codex lists claude's answer")
		}
	}
	// Nor does any other run or actor while the round is open.
	if err := read(claude, "reviews/315-x/r1-codex.md"); status(err) != http.StatusForbidden {
		t.Fatalf("an implement run reads an open round: %v", err)
	}
	// They keep off the issue's thread.
	if _, err := w.s.opComment(ctx, c1, CommentIn{Body: "leak"}); !errors.Is(err, errDebateQuiet) {
		t.Fatalf("comment in a round: %v", err)
	}
	if _, err := w.s.opIssueCreate(ctx, c1, IssueCreateIn{Title: "leak"}); !errors.Is(err, errDebateQuiet) {
		t.Fatalf("issue in a round: %v", err)
	}

	cons := debater("r-a1", "agy", "consolidate", 1)
	for _, f := range []string{"reviews/315-x/r1-claude.md", "reviews/315-x/r1-codex.md"} {
		if err := read(cons, f); err != nil {
			t.Fatalf("consolidate reads %s: %v", f, err)
		}
	}
	if _, err := put(cons, "reviews/315-x/r1-agy.md", "x\n", ""); status(err) != http.StatusForbidden {
		t.Fatalf("consolidate writes an answer: %v", err)
	}
	out, err := put(cons, "reviews/315-x/r1-consolidated.md", "# Merged\n\nBoth agree.\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(out.Changed, "Review-315-x.md") {
		t.Fatalf("consolidation did not write the page: %v", out.Changed)
	}
	page := w.read("Review-315-x").Content
	for _, want := range []string{"## Round 1, consolidated by agy", "### Merged", "Both agree.", "[codex](/bottown/demo/wiki/raw/reviews/315-x/r1-codex.md)", wiki.Open("backlinks")} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
	if n := w.git("--git-dir", w.gitDir, "rev-list", "--count", "HEAD", "--", "Review-315-x.md"); n != "1" {
		t.Errorf("page has %s revisions, want 1", n)
	}
	// The round is consolidated: it takes no more answers, and is open to
	// everyone.
	if _, err := put(debater("r-k1", "kimi", "discuss", 1), "reviews/315-x/r1-kimi.md", "late\n", ""); status(err) != http.StatusConflict {
		t.Fatalf("an answer after consolidation: %v", err)
	}
	if err := read(claude, "reviews/315-x/r1-codex.md"); err != nil {
		t.Fatalf("an implement run reads a consolidated round: %v", err)
	}
	// Round 2 reads round 1's consolidation, not its answers.
	c2 := debater("r-c2", "claude", "discuss", 2)
	if err := read(c2, "reviews/315-x/r1-consolidated.md"); err != nil {
		t.Fatal(err)
	}
	if err := read(c2, "reviews/315-x/r1-codex.md"); status(err) != http.StatusForbidden {
		t.Fatalf("round 2 reads round 1's answers: %v", err)
	}
	c2.Run.Debate.Raw = true
	if err := read(c2, "reviews/315-x/r1-codex.md"); err != nil {
		t.Fatalf("a raw round 2 reads round 1's answers: %v", err)
	}
}

// TestPlaceInDebate: a discuss run starts round 1, the others join it, a
// consolidate run waits for it to end, and the next round starts only once
// it is consolidated.
func TestPlaceInDebate(t *testing.T) {
	w := newWikiRepo(t, true)
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w.s.Store = store
	ctx := context.Background()
	now := time.Now().UTC()
	n := 0
	place := func(actor, kind string, dr DebateRequest) (secrets.Run, error) {
		n++
		run := secrets.Run{ID: "r-" + strings.Repeat("0", n), Kind: kind, Actor: actor, Org: "bottown", Repo: "demo", Issue: 315,
			Created: now.Add(time.Duration(n) * time.Second), Expires: now.Add(time.Hour)}
		err := w.s.placeInDebate(ctx, &run, RunRequest{Kind: kind, Debate: &dr}, "`Multi-agent` rounds, in the wiki")
		if err == nil {
			if _, err := store.NewRun(run, false); err != nil {
				t.Fatal(err)
			}
		}
		return run, err
	}
	end := func(id string) {
		if _, _, err := store.EndRun(id, "succeeded", time.Now().UTC(), secrets.Digest{}); err != nil {
			t.Fatal(err)
		}
	}
	answer := func(run secrets.Run) {
		p := secrets.Principal{Actor: run.Actor, Run: &run}
		if _, err := w.s.opWikiPut(ctx, p, WikiPutIn{Repo: "demo", Path: wiki.Round{Debate: run.Debate.Name, N: run.Debate.Round, Author: run.Actor}.File(), Content: "answer\n"}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := place("claude", "discuss", DebateRequest{Question: "Which?"})
	if err != nil || c.Debate.Name != "315-multi-agent-rounds-in-the" || c.Debate.Round != 1 || c.Debate.Question != "Which?" {
		t.Fatalf("first run = %+v, %v", c.Debate, err)
	}
	x, err := place("codex", "discuss", DebateRequest{})
	if err != nil || x.Debate.Name != c.Debate.Name || x.Debate.Round != 1 || x.Debate.Question != "Which?" {
		t.Fatalf("joining run = %+v, %v", x.Debate, err)
	}
	if _, err := place("agy", "discuss", DebateRequest{Question: "Other?"}); err == nil {
		t.Fatal("joined an open round with another question")
	}
	if _, err := place("claude", "discuss", DebateRequest{}); err == nil || !strings.Contains(err.Error(), "is answering") {
		t.Fatalf("second run of an actor in a round: %v", err)
	}
	answer(c)
	end(c.ID)
	if _, err := place("claude", "discuss", DebateRequest{}); err == nil || !strings.Contains(err.Error(), "has answered") {
		t.Fatalf("an actor that answered rejoins: %v", err)
	}
	if _, err := place("agy", "consolidate", DebateRequest{}); err == nil || !strings.Contains(err.Error(), "is open") {
		t.Fatalf("consolidate an open round: %v", err)
	}
	answer(x)
	end(x.ID)
	a, err := place("agy", "consolidate", DebateRequest{})
	if err != nil || a.Debate.Round != 1 || a.Debate.Question != "Which?" {
		t.Fatalf("consolidate = %+v, %v", a.Debate, err)
	}
	if _, err := place("kimi", "discuss", DebateRequest{}); err == nil || !strings.Contains(err.Error(), "being consolidated") {
		t.Fatalf("join a round being consolidated: %v", err)
	}
	ap := secrets.Principal{Actor: "agy", Run: &a}
	if _, err := w.s.opWikiPut(ctx, ap, WikiPutIn{Repo: "demo", Path: wiki.Round{Debate: a.Debate.Name, N: 1, Author: wiki.Consolidated}.File(), Content: "merged\n"}); err != nil {
		t.Fatal(err)
	}
	end(a.ID)
	if _, err := place("agy", "consolidate", DebateRequest{}); err == nil || !strings.Contains(err.Error(), "is consolidated") {
		t.Fatalf("consolidate twice: %v", err)
	}
	c2, err := place("claude", "discuss", DebateRequest{Question: "Next?", Raw: true})
	if err != nil || c2.Debate.Round != 2 || !c2.Debate.Raw || c2.Debate.Name != c.Debate.Name {
		t.Fatalf("round 2 = %+v, %v", c2.Debate, err)
	}
	// A dated debate, and an explicit slug, are separate debates.
	d, err := place("claude", "discuss", DebateRequest{Date: "2026-11-07"})
	if err != nil || d.Debate.Name != "2026-11-07-315-multi-agent-rounds-in-the" || d.Debate.Round != 1 {
		t.Fatalf("dated = %+v, %v", d.Debate, err)
	}
	s, err := place("claude", "discuss", DebateRequest{Slug: "other"})
	if err != nil || s.Debate.Name != "315-other" || s.Debate.Round != 1 {
		t.Fatalf("slugged = %+v, %v", s.Debate, err)
	}
	// Now #315 has two undated debates: a request must name one.
	if _, err := place("codex", "discuss", DebateRequest{}); err == nil || !strings.Contains(err.Error(), "several debates") {
		t.Fatalf("ambiguous debate: %v", err)
	}
	for _, bad := range []DebateRequest{{Date: "20261107"}, {Slug: "Bad Slug"}} {
		if _, err := place("codex", "discuss", bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
