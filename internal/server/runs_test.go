package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

func TestPurgeRunsRoute(t *testing.T) {
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"r-done", "r-kept"} {
		store.NewRun(secrets.Run{ID: id, Repo: "demo", Actor: "claude", Created: now.Add(-time.Hour), Expires: now.Add(time.Hour)})
		store.EndRun(id, "succeeded", now.Add(-time.Minute))
	}
	runKey, _ := store.NewRun(secrets.Run{ID: "r-live", Repo: "demo", Actor: "claude", Created: now, Expires: now.Add(time.Hour)})
	ownerKey, _ := store.NewAPIKey("owner", "test", "")
	actorKey, _ := store.NewAPIKey("claude", "test", "")
	srv := &Server{Owner: "owner", Store: store, Log: slog.Default()}
	srv.health.Reconciled = true
	h := srv.Handler()

	purge := func(key, body string) (int, string) {
		req := httptest.NewRequest("POST", "/v1/runs/purge", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	body := `{"repo":"demo","ended_before":"` + now.Format(time.RFC3339Nano) + `","keep":["r-kept"]}`
	for name, key := range map[string]string{"actor": actorKey, "run": runKey} {
		if code, _ := purge(key, body); code != http.StatusForbidden {
			t.Errorf("%s key: %d, want 403", name, code)
		}
	}
	if code, _ := purge(ownerKey, `{"repo":"demo"}`); code != http.StatusBadRequest {
		t.Errorf("no ended_before: %d, want 400", code)
	}
	code, out := purge(ownerKey, body)
	var got struct{ Purged []string }
	if code != http.StatusOK || json.Unmarshal([]byte(out), &got) != nil || len(got.Purged) != 1 || got.Purged[0] != "r-done" {
		t.Fatalf("owner purge: %d %s", code, out)
	}
	st, _ := store.Read()
	if _, ok := st.Runs["r-done"]; ok || st.Runs["r-kept"].ID == "" || st.Runs["r-live"].ID == "" {
		t.Errorf("runs after purge: %+v", st.Runs)
	}
}
