package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/server"
)

// gatherRetro writes the flow's runs, oldest first, with their local
// records (never the key), denials, prompts and normalized transcripts, and
// leaves out other issues' runs and the retro run itself.
func TestGatherRetro(t *testing.T) {
	ws := &runs.Workspace{RunsDir: t.TempDir()}
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ended := t0.Add(90 * time.Second)
	code := 1
	if err := ws.Save(runs.Record{
		ID: "r-1a", Kind: policy.Implement, Actor: "claude", Family: "anthropic", Harness: "script",
		Issue: 7, Branch: "issue/7", Status: runs.StatusFailed, ExitCode: &code, Started: t0, Ended: &ended,
		Key: "secret-run-key", FlowNote: "checks failed",
	}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a b ", (maxTranscriptField+100)/4)
	for name, body := range map[string]string{
		"r-1a.log":       "pushed the branch\n" + long + "\n",
		"r-1a.prompt.md": "You are claude.",
	} {
		if err := os.WriteFile(filepath.Join(ws.RunsDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list := []server.RunItem{
		{Run: secrets.Run{ID: "r-3c", Kind: policy.Retro, Actor: "codex", Issue: 7, Created: t0.Add(time.Hour)}, State: "active"},
		{Run: secrets.Run{ID: "r-2b", Kind: policy.Verify, Actor: "codex", Family: "openai", PR: 9, Branch: "issue/7", SHA: "0123456789abcdef", Created: t0.Add(time.Minute), Status: "succeeded", Outcome: "approved"}, State: "ended"},
		{Run: secrets.Run{ID: "r-1a", Kind: policy.Implement, Actor: "claude", Family: "anthropic", Issue: 7, Branch: "issue/7", Created: t0, Ended: &ended, Status: "failed",
			Digest: secrets.Digest{Summary: "tried", Failure: &secrets.Failure{Cause: secrets.FailureHarnessExit}}}, State: "ended"},
		{Run: secrets.Run{ID: "r-4d", Kind: policy.Implement, Actor: "claude", Issue: 8, Branch: "issue/8", Created: t0}, State: "ended"},
	}
	audit := []server.AuditEntry{
		{Run: "r-1a", Op: "api", Decision: "deny", Rule: "role-implement-review"},
		{Run: "r-4d", Op: "api", Decision: "deny"},
	}
	dir := filepath.Join(t.TempDir(), retroDir)
	n, err := gatherRetro(dir, server.NewRetroFlow(7, false, ""), "r-3c", ws, list, audit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("gatherRetro found %d runs, want 2", n)
	}

	b, err := os.ReadFile(filepath.Join(dir, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-run-key") {
		t.Errorf("runs.json holds the run key:\n%s", b)
	}
	var got []retroRun
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "r-1a" || got[1].ID != "r-2b" {
		t.Fatalf("runs = %+v, want r-1a then r-2b", got)
	}
	impl := got[0]
	if impl.Duration != "1m30s" || impl.Summary != "tried" || impl.Failure == nil || len(impl.Denials) != 1 ||
		impl.Local == nil || impl.Local.FlowNote != "checks failed" || *impl.Local.ExitCode != 1 ||
		impl.Transcript != "transcripts/r-1a.jsonl" || impl.Prompt != "prompts/r-1a.md" || impl.Harness != "script" {
		t.Errorf("implement run = %+v", impl)
	}
	if v := got[1]; v.Local != nil || v.Transcript != "" || v.Outcome != "approved" {
		t.Errorf("verify run = %+v, want no local files", v)
	}

	tr, err := os.ReadFile(filepath.Join(dir, impl.Transcript))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(tr)), "\n")
	var first, second runs.LogLine
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || json.Unmarshal([]byte(lines[1]), &second) != nil {
		t.Fatalf("transcript = %s", tr)
	}
	if first.Text != "pushed the branch" || !strings.HasSuffix(second.Text, "… [102 bytes cut]") {
		t.Errorf("transcript lines = %+v, %q", first, second.Text)
	}
	if p, _ := os.ReadFile(filepath.Join(dir, impl.Prompt)); string(p) != "You are claude." {
		t.Errorf("prompt = %q", p)
	}

	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Evidence for the retro of #7",
		"| `r-1a` | implement | claude (anthropic) | #7 | 2026-11-01T10:00:00Z | 1m30s | failed: harness_exit | - | 1 | transcripts/r-1a.jsonl | prompts/r-1a.md |",
		"| `r-2b` | verify | codex (openai) | !9 @ 0123456789 |",
	} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("README lacks %q:\n%s", want, readme)
		}
	}

	// A flow bottown never ran still gets its (empty) evidence.
	n, err = gatherRetro(dir, server.NewRetroFlow(99, false, ""), "r-3c", ws, list, audit, nil)
	if err != nil || n != 0 {
		t.Fatalf("gatherRetro(99) = %d, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "runs.json")); strings.TrimSpace(string(b)) != "[]" {
		t.Errorf("runs.json for #99 = %s, want []", b)
	}
}

// A discuss run on one of the flow's pull requests records the PR as its
// Issue, with no branch: it is in the retro whether the PR is known from a
// verify run the server lists, from one only this checkout still records,
// or from Forgejo, and discussions of other PRs are not.
func TestGatherRetroPRDiscussions(t *testing.T) {
	ws := &runs.Workspace{RunsDir: t.TempDir()}
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ended := t0.Add(time.Hour)
	// The server purged this verifier of !12; only the local record is left.
	if err := ws.Save(runs.Record{
		ID: "r-5e", Kind: policy.Verify, Actor: "codex", Family: "openai", PR: 12, Branch: "issue/7",
		Status: runs.StatusSucceeded, Started: t0.Add(5 * time.Minute), Ended: &ended,
	}); err != nil {
		t.Fatal(err)
	}
	list := []server.RunItem{
		{Run: secrets.Run{ID: "r-1a", Kind: policy.Implement, Actor: "claude", Issue: 7, Branch: "issue/7", Created: t0}, State: "ended"},
		{Run: secrets.Run{ID: "r-2b", Kind: policy.Verify, Actor: "codex", PR: 9, Branch: "issue/7", Created: t0.Add(time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-3c", Kind: policy.Discuss, Actor: "kimi", Issue: 9, Created: t0.Add(2 * time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-4d", Kind: policy.Discuss, Actor: "kimi", Issue: 11, Created: t0.Add(3 * time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-6f", Kind: policy.Discuss, Actor: "kimi", Issue: 12, Created: t0.Add(6 * time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-7g", Kind: policy.Discuss, Actor: "kimi", Issue: 13, Created: t0.Add(7 * time.Minute)}, State: "ended"},
	}
	dir := filepath.Join(t.TempDir(), retroDir)
	if _, err := gatherRetro(dir, server.NewRetroFlow(7, false, ""), "r-self", ws, list, nil, []int64{13}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []retroRun
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if want := "r-1a r-2b r-3c r-5e r-6f r-7g"; strings.Join(ids, " ") != want {
		t.Errorf("runs = %v, want %s (not r-4d, a discussion of !11)", ids, want)
	}
}

// A retro on a pull request from issue/7 gathers #7's flow, the implement
// runs included, as a retro on #7 does; one on a pull request from another
// branch gathers that pull request's runs.
func TestGatherRetroOnPR(t *testing.T) {
	ws := &runs.Workspace{RunsDir: t.TempDir()}
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	heads := []prHead{{Number: 9}, {Number: 12}, {Number: 20}, {Number: 21}}
	heads[0].Head.Ref, heads[1].Head.Ref, heads[2].Head.Ref, heads[3].Head.Ref = "issue/7", "issue/7", "hotfix", "issue/8"
	list := []server.RunItem{
		{Run: secrets.Run{ID: "r-1a", Kind: policy.Implement, Actor: "claude", Issue: 7, Branch: "issue/7", Created: t0}, State: "ended"},
		{Run: secrets.Run{ID: "r-2b", Kind: policy.Verify, Actor: "codex", PR: 9, Branch: "issue/7", Created: t0.Add(time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-3c", Kind: policy.Implement, Actor: "claude", Issue: 7, Branch: "issue/7", Created: t0.Add(2 * time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-4d", Kind: policy.Discuss, Actor: "kimi", Issue: 12, Created: t0.Add(3 * time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-5e", Kind: policy.Implement, Actor: "claude", Issue: 8, Branch: "issue/8", Created: t0.Add(4 * time.Minute)}, State: "ended"},
		{Run: secrets.Run{ID: "r-6f", Kind: policy.Verify, Actor: "codex", PR: 20, Branch: "hotfix", Created: t0.Add(5 * time.Minute)}, State: "ended"},
	}
	gather := func(n int64) ([]string, string) {
		t.Helper()
		flow, prs := retroFlowOf(n, heads)
		dir := filepath.Join(t.TempDir(), retroDir)
		if _, err := gatherRetro(dir, flow, "r-self", ws, list, nil, prs); err != nil {
			t.Fatal(err)
		}
		var got []retroRun
		b, err := os.ReadFile(filepath.Join(dir, "runs.json"))
		if err != nil || json.Unmarshal(b, &got) != nil {
			t.Fatalf("runs.json = %s, %v", b, err)
		}
		var ids []string
		for _, r := range got {
			ids = append(ids, r.ID)
		}
		readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
		return ids, string(readme)
	}
	const want = "r-1a r-2b r-3c r-4d"
	if ids, _ := gather(7); strings.Join(ids, " ") != want {
		t.Errorf("retro on #7 = %v, want %s", ids, want)
	}
	ids, readme := gather(9)
	if strings.Join(ids, " ") != want {
		t.Errorf("retro on !9 = %v, want %s, the implement runs of #7 included", ids, want)
	}
	if !strings.Contains(readme, "# Evidence for the retro of !9 of #7\n\nThe retro is on pull request !9, from `issue/7`: the evidence is the flow of issue #7.") {
		t.Errorf("README of the retro on !9:\n%s", readme)
	}
	ids, readme = gather(20)
	if strings.Join(ids, " ") != "r-6f" || !strings.Contains(readme, "# Evidence for the retro of !20\n") {
		t.Errorf("retro on !20 = %v, want r-6f:\n%s", ids, readme)
	}
}

// A retro's worktree stays when the agent left drafts in it; other kinds'
// worktrees are not kept for that.
func TestKeepRetroDrafts(t *testing.T) {
	dir := t.TempDir()
	if _, err := runs.Git(dir, nil, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if keepRetroDrafts(policy.Retro, dir) {
		t.Error("clean retro worktree kept")
	}
	if err := os.WriteFile(filepath.Join(dir, "retro-notes.md"), []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !keepRetroDrafts(policy.Retro, dir) {
		t.Error("retro worktree with a draft not kept")
	}
	if keepRetroDrafts(policy.Discuss, dir) {
		t.Error("discuss worktree kept for its changes")
	}
}

func TestRetroHandover(t *testing.T) {
	text := handoverText(runs.Record{ID: "r-1", Kind: policy.Retro, Actor: "codex", Family: "openai", Issue: 7, SHA: "abc"}, runs.StatusSucceeded, 0, "posted")
	if !strings.Contains(text, "Retrospective against `abc`, the default branch's head.") {
		t.Errorf("retro handover:\n%s", text)
	}
}
