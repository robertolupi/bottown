package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

func TestActionRefs(t *testing.T) {
	for body, want := range map[string][]string{
		"Reopen #312 after merging":                   {"Reopen #312"},
		"LGTM. Closes: #3, and fixes\nbottown/demo#4": {"Closes: #3", "fixes bottown/demo#4"},
		"(resolved #5) [reopens #6]":                  {"resolved #5", "reopens #6"},
		"fix http://forge/bottown/demo/issues/7":      {"fix http://forge/bottown/demo/issues/7"},
		"#312 should be reopened after merging":       nil,
		"the description's `Closes #312` is right":    nil,
		"```\nCloses #312\n```":                       nil,
		"prefixes #3, unclosed #4, closes the gap":    nil,
		"Part of #312":                                nil,

		// Escaped backticks are literal text, not code delimiters.
		"Escaped backticks: \\` reopen #312 \\`.": {"reopen #312"},
		"\\` Closes #3`":                       {"Closes #3"},
		"``a` closes #3":                       {"closes #3"},
		"`` a ` closes #3 ``":                  nil,
		"``Closes #3``":                        nil,
		"`a span may cross\ncloses #3 a line`": nil,
		"`no closer\n\ncloses #3 `":            {"closes #3"},
		"``` closes #3 ```":                    nil,
		"\\#3 fixes \\#4":                      {"fixes #4"},

		// Fences open at a line's start and close on their own character.
		"```go\nCloses #3\n```\nreopen #4":      {"reopen #4"},
		"````\n```\nCloses #3\n````\nreopen #4": {"reopen #4"},
		"~~~\nCloses #3\n```\n~~~\nreopen #4":   {"reopen #4"},
		"  ```\nCloses #3\n  ```":               nil,
		"    ```\nCloses #3\n```":               {"Closes #3"},
		"```\nCloses #3":                        nil,
		"text ``` Closes #3":                    {"Closes #3"},

		// Fences in list items and block quotes belong to them.
		"- ```\n  example\n  ```\n\nReopen #312 after merging.":                      {"Reopen #312"},
		"- ```\n  a\n  ```\n\nReopen #3\n\n- ```\n  b\n  ```":                        {"Reopen #3"},
		"1. ```\n   Closes #3\n   ```\n\nreopen #4":                                  {"reopen #4"},
		"> ```\n> Closes #3\n> ```\n\nreopen #4":                                     {"reopen #4"},
		"- item\n\n      Closes #3\n\nreopen #4":                                     {"reopen #4"},
		"- a list item with `Closes #3`\n  and fixes #4":                             {"fixes #4"},
		"> quoted: Reopen #3":                                                        {"Reopen #3"},
		"[Closes #3](http://forge/x) and [here](http://forge/bottown/demo/issues/7)": {"Closes #3"},
		"<div>\nFixes #3\n</div>":                                                    {"Fixes #3"},

		// Formatting around the keyword renders to the same text.
		"**Reopen** #312":                          {"Reopen #312"},
		"_Closes:_ **#3**":                         {"Closes: #3"},
		"<b>Fixes</b> #3":                          {"Fixes #3"},
		"~~resolves~~ #3":                          {"resolves #3"},
		"fix <http://forge/bottown/demo/issues/7>": {"fix http://forge/bottown/demo/issues/7"},
	} {
		if got := actionRefs(body); !reflect.DeepEqual(got, want) {
			t.Errorf("actionRefs(%q) = %q, want %q", body, got, want)
		}
	}
}

// A comment with a close or reopen reference is refused on a pull request,
// where it would act at merge, and posted on an issue; a review with one
// is refused.
func TestCommentAndReviewRefuseActionRefs(t *testing.T) {
	var posted []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			posted = append(posted, r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "body": "b"})
		case strings.HasSuffix(r.URL.Path, "/issues/9"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 9, "pull_request": map[string]any{"merged": false}})
		case strings.HasSuffix(r.URL.Path, "/issues/7"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 7})
		default:
			http.NotFound(w, r)
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
	s := &Server{Store: store, Log: slog.Default(), Rec: &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}}}
	run := &secrets.Run{ID: "r-ver", Kind: policy.Verify, Actor: "codex", Org: "bottown", Repo: "demo", PR: 9, SHA: "abc"}
	p := secrets.Principal{Actor: "codex", Run: run}
	ctx := context.Background()

	_, err = s.opComment(ctx, p, CommentIn{Target: Target{Number: 9}, Body: "Approving. Reopen #312 after merging."})
	if errStatus(err) != http.StatusBadRequest || !strings.Contains(err.Error(), `"Reopen #312"`) {
		t.Fatalf("PR comment with a reopen reference: %v", err)
	}
	if _, err := s.opComment(ctx, p, CommentIn{Target: Target{Number: 9}, Body: "#312 should be reopened after merging."}); err != nil {
		t.Fatalf("PR comment without one: %v", err)
	}
	if _, err := s.opComment(ctx, p, CommentIn{Target: Target{Number: 7}, Body: "Fixed in !9, which closes #7."}); err != nil {
		t.Fatalf("issue comment with one: %v", err)
	}
	if want := []string{"/api/v1/repos/bottown/demo/issues/9/comments", "/api/v1/repos/bottown/demo/issues/7/comments"}; !reflect.DeepEqual(posted, want) {
		t.Fatalf("posted %q, want %q", posted, want)
	}

	_, err = s.opReview(ctx, p, ReviewIn{Event: "APPROVED", CommitID: "abc", Body: "Correct. Fixes #7."})
	if errStatus(err) != http.StatusBadRequest || !strings.Contains(err.Error(), `"Fixes #7"`) {
		t.Fatalf("review with a close reference: %v", err)
	}
}
