package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// issue show in a run downloads the attachments into the run's directory
// with the run key, and lists their paths; a failed download is listed
// with its reason, and the issue is still shown.
func TestIssueShowAttachmentsInRun(t *testing.T) {
	shot := []byte("\x89PNG\r\n\x1a\nscreenshot")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer run-key" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/forge/issue":
			_ = json.NewEncoder(w).Encode(server.IssueOut{Number: 12, Title: "broken button", Body: "see screenshot", State: "open", Attachments: []server.AttachmentOut{
				{ID: 1, Name: "screen shot.png", Size: int64(len(shot))},
				{ID: 2, Name: "screen shot.png", Size: int64(len(shot)), Comment: 5},
				{ID: 3, Name: "trace.txt", Size: 4, Comment: 5},
			}})
		case "/v1/forge/issue/attachment":
			if r.URL.Query().Get("number") != "12" {
				t.Errorf("attachment of issue %s", r.URL.Query().Get("number"))
			}
			if r.URL.Query().Get("id") == "3" {
				w.WriteHeader(502)
				_, _ = w.Write([]byte(`{"error":"downloading attachment 3: forgejo GET: 500"}`))
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(shot)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	attach := filepath.Join(dir, "r-1.attachments")
	recFile := filepath.Join(dir, "r-1.json")
	ws := &runs.Workspace{RunsDir: dir}
	if err := ws.Save(runs.Record{ID: "r-1", Actor: "claude", Key: "run-key", ServerURL: srv.URL, Attachments: attach}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOTTOWN_RUN_FILE", recFile)

	show := func(args ...string) string {
		cmd := newIssue()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetArgs(append([]string{"show"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	text := show()
	for _, want := range []string{
		"see screenshot",
		"attachments (3)",
		"- `" + filepath.Join(attach, "1-screen_shot.png") + "`: \"screen shot.png\" (image/png, 18 bytes, on the description)",
		"- `" + filepath.Join(attach, "2-screen_shot.png") + "`: \"screen shot.png\" (image/png, 18 bytes, on comment 5)",
		"- \"trace.txt\" (4 bytes, on comment 5): download failed: GET /v1/forge/issue/attachment?id=3&number=12: downloading attachment 3: forgejo GET: 500",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("issue show lacks %q:\n%s", want, text)
		}
	}
	for _, id := range []string{"1", "2"} {
		if b, err := os.ReadFile(filepath.Join(attach, id+"-screen_shot.png")); err != nil || string(b) != string(shot) {
			t.Errorf("attachment %s: %q %v", id, b, err)
		}
	}
	if strings.Contains(text, "PNG") {
		t.Errorf("issue show prints contents:\n%s", text)
	}

	var got issueShowJSON
	if err := json.Unmarshal([]byte(show("--json")), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "broken button" || len(got.Attachments) != 3 ||
		got.Attachments[0].Path != filepath.Join(attach, "1-screen_shot.png") || got.Attachments[0].ContentType != "image/png" ||
		got.Attachments[2].Path != "" || !strings.Contains(got.Attachments[2].Error, "download failed") {
		t.Errorf("issue show --json: %+v", got)
	}
}
