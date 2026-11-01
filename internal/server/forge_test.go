package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

func TestParseState(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want sdk.StateType
		err  bool
	}{
		{"", sdk.StateOpen, false},
		{"open", sdk.StateOpen, false},
		{"OPEN", sdk.StateOpen, false},
		{"closed", sdk.StateClosed, false},
		{"CLOSED", sdk.StateClosed, false},
		{"all", sdk.StateAll, false},
		{"ALL", sdk.StateAll, false},
		{"invalid", "", true},
	} {
		got, err := parseState(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("parseState(%q) err = %v, wantErr = %v", tc.in, err, tc.err)
		}
		if got != tc.want {
			t.Errorf("parseState(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func testServer() *Server {
	return &Server{
		Rec: &provision.Reconciler{
			Desired: config.Desired{Org: "bottown"},
		},
	}
}

func TestResolveRepo(t *testing.T) {
	s := testServer()
	runP := secrets.Principal{
		Run: &secrets.Run{Org: "bottown", Repo: "demo"},
	}
	noRunP := secrets.Principal{}

	for _, tc := range []struct {
		name    string
		p       secrets.Principal
		repo    string
		wantOrg string
		wantRep string
		errSub  string
	}{
		// Inside run
		{name: "in run empty repo defaults", p: runP, repo: "", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run matching repo", p: runP, repo: "demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run matching org/repo", p: runP, repo: "bottown/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run matching BOTTOWN/demo", p: runP, repo: "BOTTOWN/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run other repo forbidden", p: runP, repo: "other", errSub: "this run is confined to bottown/demo"},
		{name: "in run other org/repo forbidden", p: runP, repo: "bottown/other", errSub: "this run is confined to bottown/demo"},
		{name: "in run foreign org forbidden", p: runP, repo: "foreign/demo", errSub: "this run is confined to bottown/demo"},

		// Outside run
		{name: "no run empty repo rejected", p: noRunP, repo: "", errSub: "repo is required without a run"},
		{name: "no run plain repo", p: noRunP, repo: "demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "no run org/repo", p: noRunP, repo: "bottown/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "no run case-insensitive org/repo", p: noRunP, repo: "Bottown/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "no run foreign org not found", p: noRunP, repo: "foreign/demo", errSub: "repository foreign/demo not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org, repo, err := s.resolveRepo(tc.p, tc.repo)
			if tc.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if org != tc.wantOrg || repo != tc.wantRep {
				t.Errorf("resolveRepo() = (%q, %q), want (%q, %q)", org, repo, tc.wantOrg, tc.wantRep)
			}
		})
	}
}

func TestResolveTarget(t *testing.T) {
	s := testServer()
	runP := secrets.Principal{
		Run: &secrets.Run{Org: "bottown", Repo: "demo"},
	}
	noRunP := secrets.Principal{}

	// Inside run default number
	org, repo, n, err := s.resolve(runP, Target{Number: 0}, 42)
	if err != nil || org != "bottown" || repo != "demo" || n != 42 {
		t.Errorf("in run def: got (%s, %s, %d, %v), want (bottown, demo, 42, nil)", org, repo, n, err)
	}

	// Inside run explicit number and org/repo
	org, repo, n, err = s.resolve(runP, Target{Repo: "bottown/demo", Number: 10}, 42)
	if err != nil || org != "bottown" || repo != "demo" || n != 10 {
		t.Errorf("in run explicit: got (%s, %s, %d, %v), want (bottown, demo, 10, nil)", org, repo, n, err)
	}

	// Outside run missing repo
	_, _, _, err = s.resolve(noRunP, Target{Number: 5}, 0)
	if err == nil || !strings.Contains(err.Error(), "repo is required without a run") {
		t.Errorf("outside run missing repo: want error, got: %v", err)
	}

	// Outside run missing number
	_, _, _, err = s.resolve(noRunP, Target{Repo: "demo", Number: 0}, 0)
	if err == nil || !strings.Contains(err.Error(), "number is required without a run") {
		t.Errorf("outside run missing number: want error, got: %v", err)
	}

	// Outside run with org/repo and number
	org, repo, n, err = s.resolve(noRunP, Target{Repo: "bottown/demo", Number: 15}, 0)
	if err != nil || org != "bottown" || repo != "demo" || n != 15 {
		t.Errorf("outside run valid: got (%s, %s, %d, %v), want (bottown, demo, 15, nil)", org, repo, n, err)
	}
}

func TestAPIProxyIssueDependencies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	runKey, err := store.NewRun(secrets.Run{
		ID:      "r-1",
		Kind:    policy.Implement,
		Actor:   "claude",
		Org:     "bottown",
		Repo:    "demo",
		Issue:   4,
		Created: now,
		Expires: now.Add(time.Hour),
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		Owner:      "owner",
		ForgejoURL: u,
		Store:      store,
		Rec:        &provision.Reconciler{Desired: config.Desired{Org: "bottown"}},
		Policy:     policy.Default("owner"),
		Log:        slog.Default(),
	}
	srv.health.Reconciled = true
	h := srv.Handler()

	for _, p := range []string{
		"/forgejo/api/v1/repos/bottown/demo/issues/4/dependencies",
		"/forgejo/api/v1/repos/bottown/demo/issues/4/blocks",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("Authorization", "Bearer "+runKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d (%s), want 200", p, w.Code, w.Body.String())
		}
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			req := httptest.NewRequest(method, p, strings.NewReader(`{"index":1}`))
			req.Header.Set("Authorization", "Bearer "+runKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s = %d (%s), want 403", method, p, w.Code, w.Body.String())
			}
		}
	}
}

// Complete forge reads must keep later-page context and verdicts.
func TestForgeCompleteReads(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/comments"), strings.HasSuffix(path, "/reviews"), strings.HasSuffix(path, "/pulls"), strings.HasSuffix(path, "/issues"):
			start, end := (page-1)*50, min(page*50, 51)
			if strings.HasSuffix(path, "/comments") {
				start, end = 0, 51 // This endpoint ignores page/limit.
			}
			items := []map[string]any{}
			for i := start; i < end; i++ {
				state, user := "open", "claude"
				if strings.HasSuffix(path, "/reviews") {
					state, user = "COMMENT", "codex"
					if i == 50 {
						state = "APPROVED"
					}
				}
				items = append(items, map[string]any{"id": i + 1, "number": i + 1, "body": "context", "state": state,
					"head": map[string]string{"ref": fmt.Sprintf("issue/%d", i+1), "sha": "h"},
					"user": map[string]string{"login": user}, "commit_id": "h", "official": state == "APPROVED"})
			}
			_ = json.NewEncoder(w).Encode(items)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 1, "state": "open"})
		}
	}))
	defer upstream.Close()
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"codex": {Token: "token"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: store, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown", Actors: []config.Actor{{Name: "codex", Family: "openai"}, {Name: "claude", Family: "anthropic"}}}}}
	p := secrets.Principal{Actor: "codex"}
	ctx := context.Background()
	issues, err := s.opIssueList(ctx, p, IssueListIn{Repo: "demo"})
	if err != nil || len(issues) != 51 {
		t.Fatalf("issues=%d err=%v", len(issues), err)
	}
	issue, err := s.opIssue(ctx, p, Target{Repo: "demo", Number: 1})
	if err != nil || len(issue.Comments) != 51 {
		t.Fatalf("comments=%d err=%v", len(issue.Comments), err)
	}
	pr, err := s.opPR(ctx, p, Target{Repo: "demo", Number: 1}, false)
	if err != nil || len(pr.Reviews) != 51 {
		t.Fatalf("reviews=%d err=%v", len(pr.Reviews), err)
	}
	prs, err := s.opPRList(ctx, p, PRListIn{Repo: "demo"})
	if err != nil || len(prs) != 51 || prs[50].HeadBranch != "issue/51" || prs[50].ReviewState != "approved" {
		t.Fatalf("prs=%v err=%v", prs, err)
	}
}
