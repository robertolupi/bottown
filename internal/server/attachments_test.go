package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Attachments are listed with the issue and streamed with the actor's
// token, only for issues the key may read.
func TestIssueAttachments(t *testing.T) {
	shot := []byte("\x89PNG\r\n\x1a\nimage")
	var downloads []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/attachments/") {
			downloads = append(downloads, r.URL.Path)
			if r.Header.Get("Authorization") != "token tok_claude" {
				// What Forgejo does for a request it did not authenticate.
				http.Redirect(w, r, "/user/login", http.StatusSeeOther)
				return
			}
			switch r.URL.Path {
			case "/attachments/u1":
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(shot)
			default:
				http.NotFound(w, r)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/repos/bottown/demo/issues/4", "/api/v1/repos/bottown/demo/issues/5":
			_, _ = w.Write([]byte(`{"number":4,"title":"t","body":"b","state":"open","user":{"login":"owner"}}`))
		case "/api/v1/repos/bottown/demo/issues/4/comments":
			_, _ = w.Write([]byte(`[{"id":30,"body":"logs","user":{"login":"owner"},"assets":[{"id":2,"name":"log.txt","size":5,"uuid":"u2","type":"attachment"}]}]`))
		case "/api/v1/repos/bottown/demo/issues/5/comments":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v1/repos/bottown/demo/issues/4/assets":
			_, _ = w.Write([]byte(`[{"id":1,"name":"shot.png","size":13,"uuid":"u1","type":"attachment"},{"id":3,"name":"a link","uuid":"u3","type":"external"}]`))
		case "/api/v1/repos/bottown/demo/issues/5/assets":
			http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)

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
		ID: "r-1", Kind: policy.Implement, Actor: "claude", Org: "bottown", Repo: "demo", Issue: 4,
		Created: now, Expires: now.Add(time.Hour),
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Owner: "owner", ForgejoURL: u, Store: store,
		Rec:    &provision.Reconciler{BaseURL: upstream.URL, Desired: config.Desired{Org: "bottown"}},
		Policy: policy.Default("owner"), Log: slog.Default(),
	}
	srv.health.Reconciled = true
	h := srv.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+runKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// Discovery: the description's and the comments' files, no links.
	w := get("/v1/forge/issue")
	var is IssueOut
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &is) != nil {
		t.Fatalf("issue: %d %s", w.Code, w.Body)
	}
	if len(is.Attachments) != 2 || is.AttachmentsError != "" ||
		is.Attachments[0] != (AttachmentOut{ID: 1, Name: "shot.png", Size: 13}) ||
		is.Attachments[1] != (AttachmentOut{ID: 2, Name: "log.txt", Size: 5, Comment: 30}) {
		t.Errorf("attachments %+v (%s)", is.Attachments, is.AttachmentsError)
	}
	if strings.Contains(w.Body.String(), "u1") {
		t.Errorf("issue exposes attachment UUIDs: %s", w.Body)
	}

	// A listing failure leaves the issue readable.
	w = get("/v1/forge/issue?number=5")
	is = IssueOut{}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &is) != nil || is.Title != "t" ||
		len(is.Attachments) != 0 || !strings.Contains(is.AttachmentsError, "listing attachments") {
		t.Errorf("issue with failing attachments: %d %s", w.Code, w.Body)
	}

	// Authenticated download, streamed.
	w = get("/v1/forge/issue/attachment?id=1")
	if w.Code != 200 || w.Body.String() != string(shot) || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("download: %d %q %v", w.Code, w.Body, w.Header())
	}
	if len(downloads) != 1 || downloads[0] != "/attachments/u1" {
		t.Errorf("downloads %v", downloads)
	}

	for _, tc := range []struct {
		path string
		code int
		msg  string
	}{
		{"/v1/forge/issue/attachment", 400, "id is required"},
		{"/v1/forge/issue/attachment?id=3", 404, "attachment 3 is not on issue #4"}, // a link, not a file
		{"/v1/forge/issue/attachment?id=99", 404, "attachment 99 is not on issue #4"},
		{"/v1/forge/issue/attachment?id=1&number=5", 502, "listing attachments"},
		{"/v1/forge/issue/attachment?id=2", 502, "downloading attachment 2"},
		{"/v1/forge/issue/attachment?id=1&repo=other", 403, "confined to bottown/demo"},
	} {
		if w := get(tc.path); w.Code != tc.code || !strings.Contains(w.Body.String(), tc.msg) {
			t.Errorf("GET %s = %d %s, want %d %q", tc.path, w.Code, w.Body, tc.code, tc.msg)
		}
	}

	// Without the token, Forgejo's login redirect is an error, not a file.
	srv.Store.Update(func(st *secrets.State) error {
		st.Tokens["claude"] = secrets.ActorToken{Token: "stale"}
		return nil
	})
	if w := get("/v1/forge/issue/attachment?id=1"); w.Code != 502 || !strings.Contains(w.Body.String(), "303") {
		t.Errorf("unauthenticated download: %d %s", w.Code, w.Body)
	}
}
