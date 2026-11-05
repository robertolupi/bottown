package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

func TestAuditLogRetentionAndReload(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	for _, e := range []AuditEntry{
		{Time: now.Add(-auditKeep - time.Hour), Actor: "old", Op: "api", Decision: "deny"},
		{Actor: "claude", Run: "r-1", Repo: "bottown/demo", Op: "api", Decision: "deny"},
		{Actor: "codex", Repo: "bottown/other", Op: "comment", Decision: "allow"},
	} {
		if err := a.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	got := a.Query(AuditFilter{})
	if len(got) != 2 || got[0].Actor != "claude" || got[1].Actor != "codex" {
		t.Fatalf("Query = %+v, want claude then codex (the expired entry dropped)", got)
	}
	if !got[0].Time.Equal(now) {
		t.Errorf("Time = %v, want the record time %v", got[0].Time, now)
	}

	// A restart reloads what was kept.
	b, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	b.now = a.now
	if got := b.Query(AuditFilter{}); len(got) != 2 {
		t.Fatalf("after reload: %d entries, want 2", len(got))
	}
	// Once the entries expire, nothing is returned.
	now = now.Add(auditKeep + time.Minute)
	if got := b.Query(AuditFilter{}); len(got) != 0 {
		t.Fatalf("after expiry: %+v, want none", got)
	}
}

func TestAuditLogBoundAndCompaction(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < auditMax+auditSlack+10; i++ {
		if err := a.Record(AuditEntry{Actor: "claude", Op: "api", Decision: "deny"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(a.Query(AuditFilter{})); n != auditMax {
		t.Fatalf("kept %d entries, want %d", n, auditMax)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "\n"); n > auditMax+auditSlack {
		t.Errorf("file has %d lines, want at most %d after compaction", n, auditMax+auditSlack)
	}
	if got := a.Query(AuditFilter{Limit: 3}); len(got) != 3 {
		t.Errorf("Limit 3 returned %d", len(got))
	}
}

// Entries recorded while a compaction rewrites the file outside the lock
// reach the new file, and a second compaction does not start meanwhile.
func TestAuditLogRecordsDuringCompaction(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	var snap []AuditEntry
	for i := 0; snap == nil; i++ {
		if i > auditMax+auditSlack+10 {
			t.Fatal("no compaction became due")
		}
		if snap, err = a.append(AuditEntry{Actor: "claude", Op: "api", Decision: "deny", Detail: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		s, err := a.append(AuditEntry{Actor: "codex", Op: "api", Decision: "deny", Detail: strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
		if s != nil {
			t.Fatal("a second compaction started while one was under way")
		}
	}
	if err := a.compact(snap); err != nil {
		t.Fatal(err)
	}
	if err := a.Record(AuditEntry{Actor: "kimi", Op: "api", Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	b, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := a.Query(AuditFilter{})
	got := b.Query(AuditFilter{})
	if len(got) != len(want) || len(got) != auditMax {
		t.Fatalf("reopened log has %d entries, want %d (%d in memory)", len(got), auditMax, len(want))
	}
	if n := len(b.Query(AuditFilter{Actor: "kimi"})); n != 1 {
		t.Errorf("reopened log has %d entries recorded after the compaction, want 1", n)
	}
	if n := len(b.Query(AuditFilter{Actor: "codex"})); n != 3 {
		t.Errorf("reopened log has %d of the 3 entries recorded during the compaction", n)
	}
}

// Through the proxy and the forge endpoints: denials and allowed writes are
// stored, allowed reads are not, and GET /v1/audit filters them.
func TestAuditRecordsAndFilters(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := secrets.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "codex": {Token: "tok_codex"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	runKey, err := store.NewRun(secrets.Run{
		ID: "r-1", Kind: policy.Implement, Actor: "claude", Org: "bottown", Repo: "demo",
		Issue: 4, Branch: "issue/4", Created: now, Expires: now.Add(time.Hour),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := store.NewAPIKey("owner", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	codexKey, err := store.NewAPIKey("codex", "test", secrets.ScopeRead)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Owner: "owner", ForgejoURL: u, Store: store, Audit: audit,
		Rec:    &provision.Reconciler{Desired: config.Desired{Org: "bottown"}},
		Policy: policy.Default("owner"), Log: slog.Default(),
	}
	srv.health.Reconciled = true
	h := srv.Handler()

	do := func(key, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for _, c := range []struct {
		key, method, path, body string
		want                    int
	}{
		{runKey, "GET", "/forgejo/api/v1/repos/bottown/demo/issues/4", "", 200},                          // allowed read: not stored
		{runKey, "POST", "/forgejo/api/v1/repos/bottown/demo/issues/4/comments", `{"body":"x"}`, 200},    // allowed write
		{runKey, "POST", "/forgejo/api/v1/repos/bottown/demo/issues/4/dependencies", `{"index":1}`, 403}, // reserved for bottown
		{runKey, "POST", "/forgejo/api/v1/repos/bottown/other/issues/1/comments", `{"body":"x"}`, 403},   // outside the run's repo
		{runKey, "POST", "/v1/forge/review", `{"event":"APPROVED","commit_id":"x"}`, 403},                // review in an implement run
		{runKey, "POST", "/forgejo/api/v1/repos/bottown/demo/issues/4%2Fcomments", `{"body":"x"}`, 400},  // encoded path: rejected
		{codexKey, "POST", "/forgejo/api/v1/repos/bottown/demo/issues/4/comments", `{"body":"x"}`, 403},  // read-only key
		{codexKey, "GET", "/git/bottown/demo.git/info/refs?service=git-receive-pack", "", 403},           // read-only push
	} {
		if w := do(c.key, c.method, c.path, c.body); w.Code != c.want {
			t.Fatalf("%s %s = %d (%s), want %d", c.method, c.path, w.Code, w.Body.String(), c.want)
		}
	}

	list := func(query string) []AuditEntry {
		t.Helper()
		w := do(ownerKey, "GET", "/v1/audit"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /v1/audit%s = %d (%s)", query, w.Code, w.Body.String())
		}
		var out []AuditEntry
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	all := list("")
	if len(all) != 7 {
		t.Fatalf("all = %d entries, want 7 (no allowed read): %+v", len(all), all)
	}
	if e := all[0]; e.Decision != "allow" || e.Run != "r-1" || e.Repo != "bottown/demo" || e.Method != "POST" {
		t.Errorf("first entry = %+v, want the run's allowed comment", e)
	}
	if got := list("?decision=deny"); len(got) != 5 {
		t.Errorf("decision=deny: %d entries, want 5", len(got))
	}
	runDenials := list("?run=r-1&decision=deny")
	if len(runDenials) != 3 {
		t.Fatalf("run=r-1&decision=deny: %+v, want 3", runDenials)
	}
	if e := runDenials[2]; e.Op != "review_submit" || e.Repo != "bottown/demo" {
		t.Errorf("review denial = %+v, want review_submit in bottown/demo", e)
	}
	// What the runs views ask for: a run's refusals, whatever their repo.
	refused := list("?run=r-1&decision=deny,reject")
	if len(refused) != 4 {
		t.Fatalf("run=r-1&decision=deny,reject: %+v, want 4", refused)
	}
	if e := refused[1]; e.Repo != "bottown/other" {
		t.Errorf("second refusal = %+v, want the out-of-repo comment", e)
	}
	if e := refused[3]; e.Decision != "reject" || e.Rule != "unclean-path" || e.Repo != "" {
		t.Errorf("last refusal = %+v, want the unclean-path reject", e)
	}
	if got := list("?actor=codex"); len(got) != 2 || got[1].Op != "git" || got[1].Rule != "read-only-key" {
		t.Errorf("actor=codex: %+v, want the comment and the push", got)
	}
	if got := list("?repo=other"); len(got) != 1 || got[0].Path != "/repos/bottown/other/issues/1/comments" {
		t.Errorf("repo=other: %+v, want the out-of-scope comment", got)
	}
	if got := list("?repo=bottown/demo&decision=allow"); len(got) != 1 {
		t.Errorf("repo=bottown/demo&decision=allow: %+v, want 1", got)
	}
	if got := list("?limit=2"); len(got) != 2 || got[1].Op != "git" {
		t.Errorf("limit=2: %+v, want the newest two", got)
	}
	if got := list("?since=" + url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339))); len(got) != 0 {
		t.Errorf("since the future: %+v, want none", got)
	}

	// Owner only, and bad filters are refused.
	if w := do(runKey, "GET", "/v1/audit", ""); w.Code != http.StatusForbidden {
		t.Errorf("run key GET /v1/audit = %d, want 403", w.Code)
	}
	for _, d := range []string{"maybe", "deny,maybe", "deny,"} {
		if w := do(ownerKey, "GET", "/v1/audit?decision="+d, ""); w.Code != http.StatusBadRequest {
			t.Errorf("decision=%s = %d, want 400", d, w.Code)
		}
	}
}
