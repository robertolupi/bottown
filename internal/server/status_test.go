package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

const (
	branchHead = "1111111111111111111111111111111111111111"
	pinned     = "2222222222222222222222222222222222222222"
)

func TestOpStatus(t *testing.T) {
	var mu sync.Mutex
	var posted []map[string]string
	var postedTo []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/repos/bottown/demo/branches/issue/4":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "issue/4", "commit": map[string]string{"id": branchHead}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/repos/bottown/demo/branches/"):
			http.NotFound(w, r)
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/repos/bottown/demo/statuses/"):
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			mu.Lock()
			posted = append(posted, in)
			postedTo = append(postedTo, strings.TrimPrefix(r.URL.Path, "/api/v1/repos/bottown/demo/statuses/"))
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "status": in["state"], "context": in["context"], "description": in["description"], "creator": map[string]string{"login": "claude"}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "t1"}, "codex": {Token: "t2"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, Log: slog.Default(), Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}}}
	run := func(kind, actor, branch, sha string) secrets.Principal {
		return secrets.Principal{Actor: actor, Run: &secrets.Run{ID: "r-" + kind, Kind: kind, Actor: actor, Org: "bottown", Repo: "demo", Issue: 4, PR: 9, Branch: branch, SHA: sha}}
	}
	impl, verify := run(policy.Implement, "claude", "issue/4", ""), run(policy.Verify, "codex", "", pinned)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		p    secrets.Principal
		in   CommitStatusIn
		code int
		msg  string
	}{
		{"outside a run", secrets.Principal{Actor: "claude"}, CommitStatusIn{SHA: branchHead, Check: "check", State: "success"}, 403, "needs an implement run"},
		{"discuss run", run(policy.Discuss, "claude", "", ""), CommitStatusIn{SHA: branchHead, Check: "check", State: "success"}, 403, "not allowed in a discuss run"},
		{"unknown context", impl, CommitStatusIn{SHA: branchHead, Check: "verify", State: "success"}, 400, "check must be one of check, e2e"},
		{"pending", impl, CommitStatusIn{SHA: branchHead, Check: "check", State: "pending"}, 400, "success or failure"},
		{"short sha", impl, CommitStatusIn{SHA: "1111111", Check: "check", State: "success"}, 400, "full commit id"},
		{"unpushed head", impl, CommitStatusIn{SHA: pinned, Check: "check", State: "success"}, 409, "issue/4 on the forge is at " + branchHead + ", not " + pinned + ": push HEAD first"},
		{"no branch yet", run(policy.Implement, "claude", "issue/5", ""), CommitStatusIn{SHA: branchHead, Check: "check", State: "success"}, 409, "issue/5 is not on the forge yet"},
		{"verify off its pin", verify, CommitStatusIn{SHA: branchHead, Check: "check", State: "success"}, 409, "not the commit this run verifies"},
	} {
		if _, err := s.opStatus(ctx, tc.p, tc.in); err == nil || errStatus(err) != tc.code || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: err = %v (%d), want %d %q", tc.name, err, errStatus(err), tc.code, tc.msg)
		}
	}
	if len(posted) != 0 {
		t.Fatalf("refused statuses were posted: %v", posted)
	}

	out, err := s.opStatus(ctx, impl, CommitStatusIn{SHA: branchHead, Check: "check", State: "success", Description: "passed: make check in 1m02s"})
	if err != nil || out.Context != "bottown/check" || out.State != "success" || out.Creator != "claude" {
		t.Fatalf("implement status: %+v, %v", out, err)
	}
	if _, err := s.opStatus(ctx, verify, CommitStatusIn{SHA: strings.ToUpper(pinned), Check: "e2e", State: "failure", Description: strings.Repeat("x", 300)}); err != nil {
		t.Fatalf("verify status: %v", err)
	}
	if len(posted) != 2 || postedTo[0] != branchHead || postedTo[1] != pinned ||
		posted[0]["context"] != "bottown/check" || posted[0]["description"] != "passed: make check in 1m02s (implement run r-implement)" ||
		posted[1]["context"] != "bottown/e2e" || posted[1]["state"] != "failure" || len([]rune(posted[1]["description"])) != maxStatusDescription {
		t.Errorf("posted %v to %v", posted, postedTo)
	}
}

func TestLatestStatuses(t *testing.T) {
	t0 := time.Unix(1000, 0)
	st := func(id int64, ctx, who, state string, at time.Time) *sdk.Status {
		return &sdk.Status{ID: id, Context: ctx, State: sdk.StatusState(state), Creator: &sdk.User{UserName: who}, Created: at}
	}
	got := latestStatuses([]*sdk.Status{
		st(1, "bottown/check", "claude", "failure", t0),
		st(2, "bottown/check", "claude", "success", t0.Add(time.Minute)),
		st(3, "bottown/e2e", "claude", "success", t0),
		st(4, "bottown/check", "codex", "failure", t0),
		st(6, "bottown/e2e", "claude", "failure", t0), // same time: the later id
	})
	var s []string
	for _, c := range got {
		s = append(s, c.Context+" "+c.Creator+" "+c.State)
	}
	if strings.Join(s, ", ") != "bottown/check claude success, bottown/check codex failure, bottown/e2e claude failure" {
		t.Errorf("latest statuses = %v", s)
	}
}

func TestOpPRChecks(t *testing.T) {
	statuses := `[{"id":1,"status":"success","context":"bottown/check","description":"passed","creator":{"login":"claude"},"created_at":"2026-11-03T10:00:00Z"}]`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits/"+branchHead+"/statuses"):
			_, _ = w.Write([]byte(statuses))
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			_, _ = w.Write([]byte("[]"))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 9, "state": "open", "head": map[string]string{"ref": "issue/4", "sha": branchHead}, "user": map[string]string{"login": "claude"}})
		}
	}))
	defer upstream.Close()
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"codex": {Token: "t"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}}}
	pr, err := s.opPR(context.Background(), secrets.Principal{Actor: "codex"}, Target{Repo: "demo", Number: 9}, false)
	if err != nil || len(pr.Checks) != 1 || pr.Checks[0].Context != "bottown/check" || pr.Checks[0].Creator != "claude" || pr.ChecksError != "" {
		t.Fatalf("pr checks: %+v, %q, %v", pr.Checks, pr.ChecksError, err)
	}
	statuses = `{"not": "a list"}`
	if pr, err = s.opPR(context.Background(), secrets.Principal{Actor: "codex"}, Target{Repo: "demo", Number: 9}, false); err != nil || pr.ChecksError == "" || len(pr.Checks) != 0 {
		t.Errorf("unreadable statuses: %+v, %q, %v", pr.Checks, pr.ChecksError, err)
	}
}

// The owner, outside a run, records the flow's gate in a named repository,
// and reads the PR's commits where it failed.
func TestOpGate(t *testing.T) {
	const c1, c2 = "1111111111111111111111111111111111111111", "3333333333333333333333333333333333333333"
	var posted []map[string]string
	statuses := map[string]string{
		c1: `[{"id":2,"status":"failure","context":"bottown/e2e","creator":{"login":"rlupi"},"created_at":"2026-11-03T10:00:00Z"},
		      {"id":1,"status":"success","context":"bottown/e2e","creator":{"login":"claude"},"created_at":"2026-11-03T10:00:00Z"}]`,
		c2: `[{"id":3,"status":"failure","context":"bottown/e2e","creator":{"login":"claude"},"created_at":"2026-11-03T11:00:00Z"}]`,
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/repos/bottown/other/statuses/"):
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			posted = append(posted, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "status": in["state"], "context": in["context"], "description": in["description"], "creator": map[string]string{"login": "rlupi"}})
		case r.URL.Path == "/api/v1/repos/bottown/demo/pulls/9/commits":
			_, _ = w.Write([]byte(`[{"sha":"` + c1 + `"},{"sha":"` + c2 + `"}]`))
		case strings.HasSuffix(r.URL.Path, "/statuses"):
			sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/repos/bottown/demo/commits/"), "/statuses")
			_, _ = w.Write([]byte(statuses[sha]))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"rlupi": {Token: "t0"}, "claude": {Token: "t1"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, Log: slog.Default(), Owner: "rlupi", Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}}}
	owner, ctx := secrets.Principal{Actor: "rlupi"}, context.Background()

	if _, err := s.opStatus(ctx, owner, CommitStatusIn{SHA: c1, Check: "e2e", State: "success"}); errStatus(err) != 400 || !strings.Contains(err.Error(), "repo is required") {
		t.Errorf("gate without a repo: %v", err)
	}
	out, err := s.opStatus(ctx, owner, CommitStatusIn{Repo: "other", SHA: c1, Check: "e2e", State: "failure", Description: "failed: make e2e (exit 2) after 3m"})
	if err != nil || !out.Gate || out.Context != "bottown/e2e" || len(posted) != 1 || posted[0]["description"] != "failed: make e2e (exit 2) after 3m (flow)" {
		t.Fatalf("gate status: %+v, %v, posted %v", out, err, posted)
	}
	if _, err := s.opStatus(ctx, secrets.Principal{Actor: "claude"}, CommitStatusIn{Repo: "other", SHA: c1, Check: "e2e", State: "success"}); errStatus(err) != 403 {
		t.Errorf("an actor outside a run wrote a status: %v", err)
	}

	// Only the owner's failures count: claude's own report on c2 does not.
	gate, err := s.opGate(ctx, owner, Target{Repo: "demo", Number: 9})
	if err != nil || strings.Join(gate.Failed, ",") != c1 {
		t.Fatalf("gate = %+v, %v", gate, err)
	}
	if _, err := s.opGate(ctx, secrets.Principal{Actor: "claude"}, Target{Repo: "demo", Number: 9}); errStatus(err) != 403 {
		t.Errorf("an actor read the gate: %v", err)
	}
}
