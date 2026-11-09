package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/estimate"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
)

// sizeForge is a Forgejo with one issue, #4 in bottown/demo: its labels
// and comments, and the label writes it was asked for.
type sizeForge struct {
	mu        sync.Mutex
	labels    []string
	comments  []map[string]any
	labelAdds [][]string
	failLabel bool // the next label write fails
}

func (f *sizeForge) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/repos/bottown/demo/issues/4":
			ls := []map[string]any{}
			for i, l := range f.labels {
				ls = append(ls, map[string]any{"id": i + 1, "name": l})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 4, "title": "t", "html_url": "http://forge/bottown/demo/issues/4", "labels": ls})
		case "GET /api/v1/repos/bottown/demo/issues/4/comments":
			_ = json.NewEncoder(w).Encode(f.comments)
		case "POST /api/v1/repos/bottown/demo/issues/4/comments":
			var in struct{ Body string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			id := 100 + len(f.comments)
			c := map[string]any{"id": id, "body": in.Body, "user": map[string]any{"login": r.Header.Get("X-Test-Actor")},
				"html_url": fmt.Sprintf("http://forge/bottown/demo/issues/4#issuecomment-%d", id), "created_at": time.Now()}
			f.comments = append(f.comments, c)
			_ = json.NewEncoder(w).Encode(c)
		case "POST /api/v1/repos/bottown/demo/issues/4/labels":
			var in struct{ Labels []string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			if f.failLabel {
				f.failLabel = false
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			f.labelAdds = append(f.labelAdds, in.Labels)
			f.labels = append(f.labels, in.Labels...)
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// sizeServer is a server on f whose actors' tokens name them, so the fake
// can attribute comments; run is created in its store.
func sizeServer(t *testing.T, f *sizeForge, run *secrets.Run) *Server {
	t.Helper()
	inner := f.handler(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Test-Actor", strings.TrimPrefix(r.Header.Get("Authorization"), "token tok-"))
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok-claude"}, "rlupi": {Token: "tok-rlupi"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if run != nil {
		if _, err := store.NewRun(*run, false); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{Store: store, Owner: "rlupi", Log: slog.Default(), Rec: &provision.Reconciler{BaseURL: upstream.URL,
		Desired: config.Desired{Org: "bottown", Actors: []config.Actor{{Name: "claude", Family: "anthropic"}, {Name: "rlupi"}}}}}
}

func implementRun(id string) secrets.Run {
	now := time.Now()
	return secrets.Run{ID: id, Kind: policy.Implement, Actor: "claude", Family: "anthropic", Org: "bottown", Repo: "demo",
		Issue: 4, Branch: "issue/4", Created: now, Expires: now.Add(time.Hour)}
}

func sizeM() IssueSizeIn {
	return IssueSizeIn{Input: estimate.Input{Size: "M", DecidingFactor: "one new operation", Confidence: "high", Scope: "as filed"}}
}

func outcome(t *testing.T, s *Server, id string) string {
	t.Helper()
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	return st.Runs[id].Outcome
}

func TestIssueSize(t *testing.T) {
	clarify := IssueSizeIn{Input: estimate.Input{Size: "needs-clarification", Questions: []string{"What is done?"}}}
	for _, tc := range []struct {
		name     string
		labels   []string
		in       IssueSizeIn
		want     IssueSizeOut
		adds     string // the label writes, joined
		comment  []string
		existing string
	}{
		{"unsized", nil, sizeM(),
			IssueSizeOut{Number: 4, Outcome: secrets.OutcomeSized, Size: "M", Label: "size/M", Applied: true},
			"size/M", []string{"**Estimate: size/M**", "applied = true", `run = 'r-size'`, `role = 'implement'`, `family = 'anthropic'`, "guide = '" + runs.SizingGuideVersion + "'"}, ""},
		{"already sized", []string{"mode/ask", "size/L"}, sizeM(),
			IssueSizeOut{Number: 4, Outcome: secrets.OutcomeAlreadySized, Size: "M", Label: "size/L"},
			"", []string{"_Not applied: the issue is already size/L._", "applied = false", `existing = 'size/L'`}, "size/L"},
		{"needs clarification", nil, clarify,
			IssueSizeOut{Number: 4, Outcome: secrets.OutcomeNeedsClarification, Size: "needs-clarification", Applied: true},
			"needs/clarification", []string{"@rlupi, this issue cannot be sized as written:\n\n- What is done?", `size = 'needs-clarification'`}, ""},
		{"needs clarification, already sized", []string{"size/S"}, clarify,
			IssueSizeOut{Number: 4, Outcome: secrets.OutcomeAlreadySized, Size: "needs-clarification", Label: "size/S"},
			"", []string{"- What is done?", "applied = false"}, "size/S"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &sizeForge{labels: tc.labels}
			run := implementRun("r-size")
			s := sizeServer(t, f, &run)
			got, err := s.opIssueSize(t.Context(), secrets.Principal{Actor: "claude", Run: &run}, tc.in)
			if err != nil {
				t.Fatal(err)
			}
			got.Comment, got.URL = 0, ""
			if got != tc.want {
				t.Errorf("out = %+v, want %+v", got, tc.want)
			}
			var adds []string
			for _, a := range f.labelAdds {
				adds = append(adds, a...)
			}
			if strings.Join(adds, " ") != tc.adds {
				t.Errorf("label writes = %v, want %q", adds, tc.adds)
			}
			if len(f.comments) != 1 {
				t.Fatalf("comments = %d, want 1", len(f.comments))
			}
			body := f.comments[0]["body"].(string)
			for _, want := range tc.comment {
				if !strings.Contains(body, want) {
					t.Errorf("comment lacks %q:\n%s", want, body)
				}
			}
			if rec, ok := estimate.Parse(body); !ok || rec.Existing != tc.existing {
				t.Errorf("record = %+v, %v", rec, ok)
			}
			if o := outcome(t, s, run.ID); o != tc.want.Outcome {
				t.Errorf("run outcome = %q, want %q", o, tc.want.Outcome)
			}
		})
	}
}

// A retry after the label write failed reuses the run's comment, and adds
// the label; one after it succeeded writes nothing.
func TestIssueSizeRetry(t *testing.T) {
	f := &sizeForge{failLabel: true}
	run := implementRun("r-retry")
	s := sizeServer(t, f, &run)
	p := secrets.Principal{Actor: "claude", Run: &run}
	if _, err := s.opIssueSize(t.Context(), p, sizeM()); err == nil || !strings.Contains(err.Error(), "estimate posted as comment 100") {
		t.Fatalf("failed label write: err = %v", err)
	}
	if len(f.comments) != 1 || len(f.labels) != 0 {
		t.Fatalf("after the failure: %d comments, labels %v", len(f.comments), f.labels)
	}
	// The retry's own input differs; the first estimate is the one kept.
	in := sizeM()
	in.Size = "L"
	got, err := s.opIssueSize(t.Context(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	want := IssueSizeOut{Number: 4, Outcome: secrets.OutcomeSized, Size: "M", Label: "size/M", Applied: true, Comment: 100, Reused: true,
		URL: "http://forge/bottown/demo/issues/4#issuecomment-100"}
	if got != want {
		t.Errorf("retry = %+v, want %+v", got, want)
	}
	if len(f.comments) != 1 || strings.Join(f.labels, " ") != "size/M" {
		t.Errorf("after the retry: %d comments, labels %v", len(f.comments), f.labels)
	}
	if _, err := s.opIssueSize(t.Context(), p, in); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 || len(f.labelAdds) != 1 {
		t.Errorf("after a second retry: %d comments, label writes %v", len(f.comments), f.labelAdds)
	}
}

// Another run's estimate, or a comment someone else wrote with this run's
// ID in it, is not this run's.
func TestIssueSizeOtherRunsComments(t *testing.T) {
	run := implementRun("r-mine")
	other := estimate.Comment(estimate.Input{Size: "S"}, estimate.Record{Version: 1, Run: "r-other", Actor: "claude", Size: "S", Applied: true}, "")
	forged := estimate.Comment(estimate.Input{Size: "S"}, estimate.Record{Version: 1, Run: "r-mine", Actor: "claude", Size: "S", Applied: true}, "")
	f := &sizeForge{comments: []map[string]any{
		{"id": 1, "body": other, "user": map[string]any{"login": "claude"}},
		{"id": 2, "body": forged, "user": map[string]any{"login": "kimi"}},
	}}
	s := sizeServer(t, f, &run)
	got, err := s.opIssueSize(t.Context(), secrets.Principal{Actor: "claude", Run: &run}, sizeM())
	if err != nil {
		t.Fatal(err)
	}
	if got.Reused || got.Label != "size/M" || len(f.comments) != 3 {
		t.Errorf("out = %+v, %d comments", got, len(f.comments))
	}
}

// An implement run's outcome is its pull request: an estimate does not
// replace it.
func TestIssueSizeKeepsPROutcome(t *testing.T) {
	run := implementRun("r-late")
	s := sizeServer(t, &sizeForge{}, &run)
	if _, err := s.Store.SetOutcome(run.ID, secrets.OutcomePROpened); err != nil {
		t.Fatal(err)
	}
	if _, err := s.opIssueSize(t.Context(), secrets.Principal{Actor: "claude", Run: &run}, sizeM()); err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, s, run.ID); o != secrets.OutcomePROpened {
		t.Errorf("outcome = %q, want %q", o, secrets.OutcomePROpened)
	}
}

func TestIssueSizeOwner(t *testing.T) {
	f := &sizeForge{}
	s := sizeServer(t, f, nil)
	in := sizeM()
	in.Target = Target{Repo: "demo", Number: 4}
	got, err := s.opIssueSize(t.Context(), secrets.Principal{Actor: "rlupi"}, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != secrets.OutcomeSized || got.Label != "size/M" {
		t.Errorf("out = %+v", got)
	}
	rec, ok := estimate.Parse(f.comments[0]["body"].(string))
	if !ok || rec.Role != "owner" || rec.Run != "" || rec.Actor != "rlupi" {
		t.Errorf("record = %+v, %v", rec, ok)
	}
	if strings.Contains(f.comments[0]["body"].(string), "@rlupi") {
		t.Error("the owner's own estimate mentions the owner")
	}
}

func TestIssueSizeRefused(t *testing.T) {
	now := time.Now()
	plan := secrets.Run{ID: "r-plan", Kind: policy.Plan, Actor: "claude", Org: "bottown", Repo: "demo", Issue: 4, Created: now, Expires: now.Add(time.Hour)}
	verify := secrets.Run{ID: "r-verify", Kind: policy.Verify, Actor: "claude", Org: "bottown", Repo: "demo", PR: 5, Created: now, Expires: now.Add(time.Hour)}
	impl := implementRun("r-impl")
	bad := sizeM()
	bad.Confidence = ""
	other := sizeM()
	other.Number = 7
	for _, tc := range []struct {
		name   string
		p      secrets.Principal
		in     IssueSizeIn
		status int
		want   string
	}{
		{"plan run", secrets.Principal{Actor: "claude", Run: &plan}, sizeM(), http.StatusForbidden, "not allowed in a plan run"},
		{"verify run", secrets.Principal{Actor: "claude", Run: &verify}, sizeM(), http.StatusForbidden, "not allowed in a verify run"},
		{"another issue", secrets.Principal{Actor: "claude", Run: &impl}, other, http.StatusForbidden, "only its issue, #4"},
		{"actor outside a run", secrets.Principal{Actor: "claude"}, sizeM(), http.StatusForbidden, "needs an implement run"},
		{"read-only owner key", secrets.Principal{Actor: "rlupi", ReadOnly: true}, sizeM(), http.StatusForbidden, "needs an implement run"},
		{"invalid estimate", secrets.Principal{Actor: "claude", Run: &impl}, bad, http.StatusBadRequest, "confidence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &sizeForge{}
			s := sizeServer(t, f, nil)
			_, err := s.opIssueSize(t.Context(), tc.p, tc.in)
			if err == nil || errStatus(err) != tc.status || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v (status %d), want %d %q", err, errStatus(err), tc.status, tc.want)
			}
			if len(f.comments) != 0 || len(f.labelAdds) != 0 {
				t.Errorf("wrote %d comments, labels %v", len(f.comments), f.labelAdds)
			}
		})
	}
}
