package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/review"
	"github.com/robertolupi/bottown/internal/review/reviewtest"
	"github.com/robertolupi/bottown/internal/secrets"
)

// TestCarryOver reconciles, then runs pr show on each head move of the shared
// patch cases. agy's approval at the old head was dismissed by Forgejo for the
// new commits, kimi's by the owner. When the patch is unchanged agy's approval
// carries over and is undismissed, so Forgejo counts it too; kimi's dismissal
// stands. Otherwise nothing changes.
func TestCarryOver(t *testing.T) {
	for _, tc := range reviewtest.PatchCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "bottown"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.GitDir, filepath.Join(root, "bottown", "demo.git")); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var undismissed []string
			dismissed := map[int64]bool{1: true, 2: true}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				p := r.URL.Path
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(p, "/undismissals"):
					if r.Header.Get("Authorization") != "token tok_owner" {
						http.Error(w, "Must be repo admin", http.StatusForbidden)
						return
					}
					parts := strings.Split(p, "/")
					id := parts[len(parts)-2]
					n, _ := strconv.ParseInt(id, 10, 64)
					undismissed = append(undismissed, id)
					dismissed[n] = false
					_, _ = w.Write([]byte("{}"))
				case strings.HasSuffix(p, "/timeline"):
					_ = json.NewEncoder(w).Encode([]map[string]any{
						{"id": 10, "type": "pull_push"},
						{"id": 11, "type": "dismiss_review", "review_id": 1, "body": forgejo.AutoDismissMessage},
						{"id": 12, "type": "dismiss_review", "review_id": 2, "body": "not this one"},
					})
				case strings.HasSuffix(p, "/reviews"):
					if r.URL.Query().Get("page") != "1" {
						_, _ = w.Write([]byte("[]"))
						return
					}
					_ = json.NewEncoder(w).Encode([]map[string]any{
						{"id": 1, "state": "APPROVED", "user": map[string]string{"login": "agy"}, "commit_id": tc.Old, "official": true, "stale": true, "dismissed": dismissed[1]},
						{"id": 2, "state": "APPROVED", "user": map[string]string{"login": "kimi"}, "commit_id": tc.Old, "official": true, "stale": true, "dismissed": dismissed[2]},
					})
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"number": 12, "state": "open", "user": map[string]string{"login": "claude"},
						"head": map[string]string{"ref": "issue/1", "sha": tc.Head}, "base": map[string]string{"ref": "main"}})
				}
			}))
			defer upstream.Close()
			store, err := secrets.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Update(func(st *secrets.State) error {
				st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "rlupi": {Token: "tok_owner"}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s := &Server{Owner: "rlupi", Store: store, Log: slog.Default(), GitRoot: root, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown",
				Actors: []config.Actor{{Name: "claude", Family: "anthropic"}, {Name: "agy", Family: "google"}, {Name: "kimi", Family: "moonshot"}}}}}
			if _, err := s.opReconcileCarries(context.Background(), secrets.Principal{Actor: "rlupi"}, Target{Repo: "demo", Number: 12}); err != nil {
				t.Fatal(err)
			}
			pr, err := s.opPR(context.Background(), secrets.Principal{Actor: "claude"}, Target{Repo: "demo", Number: 12}, false)
			if err != nil {
				t.Fatal(err)
			}
			a := pr.Assessment
			if !tc.Same {
				if a.Decision != review.NeedsReview || len(undismissed) != 0 || len(pr.Carries) != 0 {
					t.Fatalf("assessment = %+v, carries %v, undismissed %v; want needs review and none", a, pr.Carries, undismissed)
				}
				return
			}
			if a.Decision != review.Approved || a.Review.ID != 1 || a.Carried == nil || a.Carried.From != tc.Old || !strings.Contains(a.Reason, "carried over") {
				t.Fatalf("assessment = %+v, want agy's approval carried over", a)
			}
			if len(pr.Carries) != 1 || pr.Carries[0] != *a.Carried {
				t.Fatalf("carries = %+v, want only %+v", pr.Carries, *a.Carried)
			}
			if !slices.Equal(undismissed, []string{"1"}) {
				t.Fatalf("undismissed %v, want only Forgejo's own dismissal", undismissed)
			}
			if pr.Reviews[0].Dismissed || !pr.Reviews[1].Dismissed {
				t.Fatalf("reviews = %+v", pr.Reviews)
			}
		})
	}
}

func TestPRListCarryOverReadOnlyAndCached(t *testing.T) {
	var patch reviewtest.PatchCase
	for _, tc := range reviewtest.PatchCases(t) {
		if tc.Same {
			patch = tc
			break
		}
	}
	if patch.GitDir == "" {
		t.Fatal("no same-patch fixture")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bottown"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(patch.GitDir, filepath.Join(root, "bottown", "demo.git")); err != nil {
		t.Fatal(err)
	}

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	gitLog := filepath.Join(bin, "calls")
	wrapper := fmt.Sprintf("#!/bin/sh\nprintf 'x\\n' >> %q\nexec %q \"$@\"\n", gitLog, realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitCalls := func() int {
		b, err := os.ReadFile(gitLog)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "\n")
	}

	var mu sync.Mutex
	writes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			writes++
			_, _ = w.Write([]byte("{}"))
			return
		}
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/pulls"):
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 12, "state": "open", "user": map[string]string{"login": "claude"},
				"head": map[string]string{"ref": "issue/1", "sha": patch.Head}, "base": map[string]string{"ref": "main"}}})
		case strings.HasSuffix(p, "/reviews"):
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "state": "APPROVED", "user": map[string]string{"login": "agy"},
				"commit_id": patch.Old, "official": true, "stale": true, "dismissed": true}})
		case strings.HasSuffix(p, "/timeline"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 11, "type": "dismiss_review", "review_id": 1, "body": forgejo.AutoDismissMessage}})
		default:
			t.Errorf("unexpected upstream request %s %s", r.Method, p)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(st *secrets.State) error {
		st.Tokens = map[string]secrets.ActorToken{"claude": {Token: "tok_claude"}, "rlupi": {Token: "tok_owner"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Owner: "rlupi", Store: store, Log: slog.Default(), GitRoot: root, Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown",
		Actors: []config.Actor{{Name: "claude", Family: "anthropic"}, {Name: "agy", Family: "google"}}}}}
	ctx := context.Background()
	p := secrets.Principal{Actor: "claude"}
	if prs, err := s.opPRList(ctx, p, PRListIn{Repo: "demo"}); err != nil || len(prs) != 1 {
		t.Fatalf("first list = %+v, %v", prs, err)
	}
	first := gitCalls()
	if first == 0 {
		t.Fatal("first list ran no git subprocesses; cache test did not exercise carry-over")
	}
	if prs, err := s.opPRList(ctx, p, PRListIn{Repo: "demo"}); err != nil || len(prs) != 1 {
		t.Fatalf("second list = %+v, %v", prs, err)
	}
	if second := gitCalls(); second != first {
		t.Fatalf("second list ran %d git subprocesses; want 0", second-first)
	}
	mu.Lock()
	defer mu.Unlock()
	if writes != 0 {
		t.Fatalf("pr list made %d Forgejo writes; want 0", writes)
	}
}
