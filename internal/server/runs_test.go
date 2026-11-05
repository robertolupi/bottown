package server

import (
	"encoding/json"
	"log/slog"
	"maps"
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
		store.NewRun(secrets.Run{ID: id, Repo: "demo", Actor: "claude", Created: now.Add(-time.Hour), Expires: now.Add(time.Hour)}, false)
		store.EndRun(id, "succeeded", now.Add(-time.Minute), secrets.Digest{Summary: "done " + id})
	}
	runKey, _ := store.NewRun(secrets.Run{ID: "r-live", Repo: "demo", Actor: "claude", Created: now, Expires: now.Add(time.Hour)}, false)
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

	get := func(key, path string) (int, []secrets.Run) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var runs []secrets.Run
		json.Unmarshal(w.Body.Bytes(), &runs)
		return w.Code, runs
	}
	ids := func(runs []secrets.Run) string {
		var s []string
		for _, r := range runs {
			s = append(s, r.ID)
		}
		return strings.Join(s, ",")
	}
	for path, want := range map[string]string{
		"/v1/runs?repo=demo":                       "r-live,r-kept",
		"/v1/runs?repo=demo&history=1":             "r-live,r-done,r-kept",
		"/v1/runs?repo=demo&history=1&state=ended": "r-done,r-kept",
		"/v1/runs?state=live":                      "r-live",
		"/v1/runs?repo=other&history=1":            "",
		"/v1/runs?history=1&since=" + now.Add(-time.Minute).UTC().Format(time.RFC3339): "r-live",
	} {
		if code, runs := get(ownerKey, path); code != http.StatusOK || ids(runs) != want {
			t.Errorf("GET %s: %d %s, want %s", path, code, ids(runs), want)
		}
	}
	for _, path := range []string{"/v1/runs?state=dead", "/v1/runs?since=yesterday"} {
		if code, _ := get(ownerKey, path); code != http.StatusBadRequest {
			t.Errorf("GET %s: %d, want 400", path, code)
		}
	}
	// The purged run keeps its digest, in the list and by id.
	if _, runs := get(ownerKey, "/v1/runs?history=1&state=ended"); len(runs) != 2 || !runs[0].Purged || runs[0].Summary != "done r-done" || runs[1].Purged {
		t.Errorf("purged digest: %+v", runs)
	}
	req := httptest.NewRequest("GET", "/v1/runs/r-done", nil)
	req.Header.Set("Authorization", "Bearer "+actorKey)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"purged":true`) {
		t.Errorf("GET purged run as its actor: %d %s", w.Code, w.Body.String())
	}
}

func TestEndRunDigest(t *testing.T) {
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	runKey, _ := store.NewRun(secrets.Run{ID: "r-1", Repo: "demo", Actor: "claude", Created: now, Expires: now.Add(time.Hour)}, false)
	srv := &Server{Owner: "owner", Store: store, Log: slog.Default()}
	srv.health.Reconciled = true
	req := httptest.NewRequest("DELETE", "/v1/runs/r-1", strings.NewReader(
		`{"status":"succeeded","summary":"Opened PR !4.","usage":{"output_tokens":120,"cost_usd":0.5}}`))
	req.Header.Set("Authorization", "Bearer "+runKey)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var run secrets.Run
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &run) != nil {
		t.Fatalf("end: %d %s", w.Code, w.Body.String())
	}
	if run.Status != "succeeded" || run.Summary != "Opened PR !4." || *run.Usage.OutputTokens != 120 ||
		*run.Usage.CostUSD != 0.5 || run.Usage.InputTokens != nil {
		t.Fatalf("ended run: %+v %+v", run, run.Usage)
	}
}

// GET /v1/runs decides each run's state once, so its clients (the web UI,
// the CLI) do not each decide what counts as live.
func TestListRunsStates(t *testing.T) {
	store, err := secrets.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	store.NewRun(secrets.Run{ID: "r-active", Repo: "demo", Actor: "claude", Created: now, Expires: now.Add(time.Hour)}, false)
	store.NewRun(secrets.Run{ID: "r-expired", Repo: "demo", Actor: "claude", Created: now.Add(-2 * time.Hour), Expires: now.Add(-time.Hour)}, false)
	store.NewRun(secrets.Run{ID: "r-ended", Repo: "demo", Actor: "claude", Created: now.Add(-3 * time.Hour), Expires: now.Add(time.Hour)}, false)
	store.EndRun("r-ended", "succeeded", now, secrets.Digest{})
	ownerKey, _ := store.NewAPIKey("owner", "test", "")
	srv := &Server{Owner: "owner", Store: store, Log: slog.Default()}
	srv.health.Reconciled = true
	h := srv.Handler()

	get := func(path string) (int, map[string]string) {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+ownerKey)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var runs []RunItem
		json.Unmarshal(w.Body.Bytes(), &runs)
		states := map[string]string{}
		for _, r := range runs {
			states[r.ID] = r.State
		}
		return w.Code, states
	}
	all := map[string]string{"r-active": "active", "r-expired": "expired", "r-ended": "ended"}
	for path, want := range map[string]map[string]string{
		"/v1/runs":               all,
		"/v1/runs?state=live":    {"r-active": "active", "r-expired": "expired"},
		"/v1/runs?state=active":  {"r-active": "active"},
		"/v1/runs?state=expired": {"r-expired": "expired"},
		"/v1/runs?state=ended":   {"r-ended": "ended"},
	} {
		if code, got := get(path); code != http.StatusOK || !maps.Equal(got, want) {
			t.Errorf("GET %s: %d %v, want %v", path, code, got, want)
		}
	}
}
