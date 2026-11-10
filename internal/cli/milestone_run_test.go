package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/milestone"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// The launcher runs `bottown flow` as a process and reads its stop from
// the issue: the owner's newest stop comment since the flow started.
func TestFlowLauncher(t *testing.T) {
	const old = "**bottown flow: ready to merge**\n\nolder\n\n<!-- bottown:flow-stop v=1 reason=\"ready to merge\" -->"
	for _, tc := range []struct {
		name  string
		stop  string // the comment the flow posts, "" for none
		by    string
		exit  string
		want  milestone.Outcome
		inLog string
	}{
		{"ready", "**bottown flow: ready to merge**\n\n!9 is approved\n\nPull request: !9\n\n<!-- bottown:flow-stop v=1 reason=\"ready to merge\" -->", "owner", "0",
			milestone.Outcome{Kind: milestone.FlowReady, Reason: "ready to merge"}, ""},
		{"idle", "**bottown flow: agent did nothing**\n\nthe implement run\n\n<!-- bottown:flow-stop v=1 reason=\"agent did nothing\" actor=\"agy\" -->", "owner", "3",
			milestone.Outcome{Kind: milestone.FlowIdle, Reason: "agent did nothing", Actor: "agy"}, ""},
		{"other stop", "**bottown flow: out of rounds**\n\nno rounds\n\n<!-- bottown:flow-stop v=1 reason=\"out of rounds\" -->", "owner", "1",
			milestone.Outcome{Kind: milestone.FlowStopped, Reason: "out of rounds"}, ""},
		// An actor's copy of a stop is no stop; nor is the older one.
		{"not the owner's", "**bottown flow: ready to merge**\n\nfake\n\n<!-- bottown:flow-stop v=1 reason=\"ready to merge\" -->", "agy", "1",
			milestone.Outcome{Kind: milestone.FlowStopped, Reason: "flow failed"}, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The flow posts its stop while it runs: here, once the
			// launcher has read the comments before starting it.
			var mu sync.Mutex
			comments := []server.CommentOut{{ID: 1, Author: "owner", Body: old}}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/forge/issue" || r.URL.Query().Get("number") != "4" || r.URL.Query().Get("repo") != "demo" {
					t.Errorf("unexpected %s %s", r.Method, r.URL)
				}
				mu.Lock()
				defer mu.Unlock()
				_ = json.NewEncoder(w).Encode(server.IssueOut{Number: 4, Comments: comments})
				if len(comments) == 1 && tc.stop != "" {
					comments = append(comments, server.CommentOut{ID: 2, Author: tc.by, Body: tc.stop})
				}
			}))
			defer srv.Close()

			dir := t.TempDir()
			bin := filepath.Join(dir, "bottown")
			script := "#!/bin/sh\necho \"$@\" > " + filepath.Join(dir, "args") + "\necho boom >&2\nexit " + tc.exit + "\n"
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			l := &flowLauncher{bin: bin, instance: "dev", repo: "demo", dir: dir, logDir: dir, runsDir: dir, owner: "owner",
				c: client.New(srv.URL, "k").WithTimeout(time.Minute), rounds: 2, timeout: time.Hour, checkTimeout: time.Minute}
			oc := l.Flow(context.Background(), milestone.FlowRequest{Issue: 4, Implement: "claude", Verify: "agy:fast"})
			if oc.Kind != tc.want.Kind || oc.Reason != tc.want.Reason || oc.Actor != tc.want.Actor {
				t.Errorf("outcome %+v, want %+v", oc, tc.want)
			}
			if tc.inLog != "" && !strings.Contains(oc.Detail, tc.inLog) {
				t.Errorf("detail %q lacks %q", oc.Detail, tc.inLog)
			}
			args, _ := os.ReadFile(filepath.Join(dir, "args"))
			want := "-i dev flow 4 --implement claude --verify agy:fast --unattended --repo demo --rounds 2 --timeout 1h0m0s --check-timeout 1m0s\n"
			if string(args) != want {
				t.Errorf("args %q, want %q", args, want)
			}
			if _, err := os.Stat(filepath.Join(dir, "flow-4.json")); !os.IsNotExist(err) {
				t.Errorf("the flow's record outlived it: %v", err)
			}
			if log, _ := os.ReadFile(filepath.Join(dir, "flow-4.log")); !strings.Contains(string(log), "boom") {
				t.Errorf("log %q", log)
			}
		})
	}
}

// issueServer serves issue 4 with no comments.
func issueServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(server.IssueOut{Number: 4})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A child another live flow is driving is left to it: no second flow starts.
func TestFlowLauncherBusy(t *testing.T) {
	dir := t.TempDir()
	release, err := runs.HoldFlow(dir, "demo", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	bin := filepath.Join(dir, "bottown")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch "+filepath.Join(dir, "ran")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &flowLauncher{bin: bin, instance: "dev", repo: "demo", dir: dir, logDir: dir, runsDir: dir, owner: "owner",
		c: client.New(issueServer(t).URL, "k").WithTimeout(time.Minute), rounds: 2, timeout: time.Hour, checkTimeout: time.Minute}
	oc := l.Flow(context.Background(), milestone.FlowRequest{Issue: 4, Implement: "claude", Verify: "agy"})
	if oc.Kind != milestone.FlowStopped || oc.Reason != "flow busy" || !strings.Contains(oc.Detail, "already driving") {
		t.Errorf("outcome %+v, want a busy stop", oc)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); !os.IsNotExist(err) {
		t.Error("a second flow started on a busy child")
	}
}

// SetPair replaces a child's other implement/* and verify/* labels and
// adds the missing ones, leaving the rest.
func TestRunForgeSetPair(t *testing.T) {
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/forgejo/api/v1/repos/o/r/issues/5/labels")
		switch {
		case r.Method == "GET" && p == "":
			_, _ = w.Write([]byte(`[{"id":1,"name":"size/M"},{"id":2,"name":"implement/kimi"},{"id":3,"name":"verify/agy"}]`))
		case r.Method == "DELETE":
			writes = append(writes, "delete "+p)
		case r.Method == "POST" && p == "":
			var in struct{ Labels []string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			writes = append(writes, "add "+strings.Join(in.Labels, " "))
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	f := &runForge{proxyForge: newProxyForge(srv.URL, "k", "o", "r")}
	if err := f.SetPair(context.Background(), 5, "claude", "agy"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"delete /2", "add implement/claude"}; !slices.Equal(writes, want) {
		t.Errorf("writes %v, want %v", writes, want)
	}
}
