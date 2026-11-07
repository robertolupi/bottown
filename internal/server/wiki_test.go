package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/wiki"
)

// wikiRepo is a wiki's bare repository under a test GitRoot, with a
// server whose pushes are compare-and-swaps on the branch (what Forgejo's
// refusal of a non-fast-forward push amounts to) and whose wiki creation
// commits a placeholder Home page, as Forgejo's API does.
type wikiRepo struct {
	t      *testing.T
	s      *Server
	gitDir string
	// beforePush runs before a push, once per push, to move the branch the
	// way an edit in the web UI would.
	beforePush func()
}

var (
	owner  = secrets.Principal{Actor: "rlupi"}
	claude = secrets.Principal{Actor: "claude", Run: &secrets.Run{ID: "r-1", Kind: "implement", Org: "bottown", Repo: "demo"}}
	reader = secrets.Principal{Actor: "codex", ReadOnly: true}
)

func newWikiRepo(t *testing.T, exists bool) *wikiRepo {
	t.Helper()
	root := t.TempDir()
	w := &wikiRepo{t: t, gitDir: filepath.Join(root, "bottown", "demo.wiki.git")}
	w.s = &Server{Owner: "rlupi", Log: slog.Default(), GitRoot: root,
		Rec: &provision.Reconciler{Desired: config.Desired{Org: "bottown"}}}
	w.s.wikiPushFn = func(ctx context.Context, gitDir, branch, old, commit string) error {
		if w.beforePush != nil {
			f := w.beforePush
			w.beforePush = nil
			f()
		}
		_, err := mergeGit(ctx, gitDir, nil, nil, "update-ref", "refs/heads/"+branch, commit, old)
		return err
	}
	w.s.wikiCreateFn = func(ctx context.Context, org, repo string) error {
		if err := os.MkdirAll(filepath.Dir(w.gitDir), 0o755); err != nil {
			return err
		}
		w.git("init", "-q", "--bare", "-b", "main", w.gitDir)
		w.commit("Home.md", "placeholder\n", freshMessage)
		return nil
	}
	if exists {
		if err := w.s.wikiCreateFn(context.Background(), "bottown", "demo"); err != nil {
			t.Fatal(err)
		}
		w.commit("Home.md", "# Home\n\nSee [Idea](Idea).\n", "owner writes Home")
	}
	return w
}

func (w *wikiRepo) git(args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		w.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes file on the wiki's branch directly, as Forgejo's web UI
// does: no generated blocks are refreshed.
func (w *wikiRepo) commit(file, content, msg string) string {
	w.t.Helper()
	work := w.t.TempDir()
	w.git("clone", "-q", w.gitDir, work)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(work, file)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, file), []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
	w.git("-C", work, "add", "-A")
	w.git("-C", work, "commit", "-q", "-m", msg)
	w.git("-C", work, "push", "-q", "origin", "HEAD:main")
	return w.git("-C", work, "rev-parse", "HEAD")
}

func (w *wikiRepo) read(file string) WikiReadOut {
	w.t.Helper()
	out, err := w.s.opWikiRead(context.Background(), owner, "demo", file)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *wikiRepo) head() string { return w.git("--git-dir", w.gitDir, "rev-parse", "main") }

func status(err error) int {
	var fe *forgeError
	if errors.As(err, &fe) {
		return fe.status
	}
	var ce *wikiConflict
	if errors.As(err, &ce) {
		return http.StatusConflict
	}
	return 0
}

func TestWikiPutCompareAndSwap(t *testing.T) {
	w := newWikiRepo(t, true)
	ctx := context.Background()
	// Create: no base. The commit fills the page's blocks and every other
	// page's, and creates the health page.
	put, err := w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "Idea", Content: "# Idea\n\nBack to [Home](Home).\n"})
	if err != nil {
		t.Fatal(err)
	}
	if put.Path != "Idea.md" || put.Commit == "" || strings.Join(put.Changed, ",") != "Home.md,Idea.md,Wiki-Health.md" {
		t.Fatalf("put = %+v", put)
	}
	idea := w.read("Idea")
	if idea.SHA != put.SHA || !strings.Contains(idea.Content, wiki.Open("backlinks")+"\n**Linked from:** [Home](Home)") {
		t.Fatalf("read = %+v (put sha %s)", idea, put.SHA)
	}
	// Create again: the page exists.
	if _, err := w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: "x\n"}); status(err) != http.StatusConflict {
		t.Fatalf("create over an existing page: %v", err)
	}
	// Update at the blob read, sending a stale block: the block is refilled.
	stale := strings.Replace(idea.Content, "**Linked from:**", "stale", 1) + "More.\n"
	put, err = w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: stale, Base: idea.SHA})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.read("Idea").Content; strings.Contains(got, "stale") || !strings.Contains(got, "More.") {
		t.Fatalf("after update: %q", got)
	}
	// The owner edits the page's text in the web UI: a put at the old
	// blob is a conflict and names the new one.
	w.commit("Idea.md", w.read("Idea").Content+"Owner's edit.\n", "owner edits Idea")
	_, err = w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: "lost\n", Base: put.SHA})
	if status(err) != http.StatusConflict || !strings.Contains(err.Error(), "read it again") {
		t.Fatalf("stale base: %v", err)
	}
	// A change to generated blocks alone since the read is no conflict.
	cur := w.read("Idea")
	w.commit("Home.md", "# Home\n\nSee [Idea](Idea).\n\n[Other](Other)\n", "owner adds a link to Home")
	w.commit("Other.md", "Other links to [Idea](Idea)\n", "owner adds Other")
	if _, err := w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: cur.Content, Base: cur.SHA}); err != nil {
		t.Fatalf("put after a generate-only change: %v", err)
	}
	bl := w.read("Idea").Content
	if !strings.Contains(bl, "[Home](Home), [Other](Other)") {
		t.Fatalf("backlinks not refreshed: %q", bl)
	}
	gen, err := w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if gen.Commit != "" || len(gen.Changed) != 0 {
		t.Fatalf("a put left blocks stale: %+v", gen)
	}
	// Nothing to change: no commit.
	before := w.head()
	cur = w.read("Idea")
	put, err = w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: cur.Content, Base: cur.SHA})
	if err != nil || !put.Unchanged || w.head() != before || put.SHA != cur.SHA {
		t.Fatalf("no-op put = %+v, %v; head %s -> %s", put, err, before, w.head())
	}
	// Bad writes.
	for _, in := range []WikiPutIn{
		{Path: "../x.md", Content: "x"},
		{Path: ".git/config", Content: "x"},
		{Path: "notes.txt", Content: "x"},
		{Path: "Bad.md", Content: wiki.Open("backlinks")},
		{Path: "Bad.md", Content: wiki.Open("nope") + wiki.Close("nope")},
	} {
		in.Repo = "demo"
		if _, err := w.s.opWikiPut(ctx, owner, in); status(err) != http.StatusBadRequest {
			t.Errorf("put %q %q: %v, want 400", in.Path, in.Content, err)
		}
	}
}

// TestWikiPutRetriesOnRace: the owner saves a page in the web UI while a
// put is in flight. The put's push is refused, and the put is rebuilt on
// the new head: the owner's edit survives, and so does the put.
func TestWikiPutRetriesOnRace(t *testing.T) {
	w := newWikiRepo(t, true)
	w.beforePush = func() { w.commit("Owner.md", "made in the UI, links [Idea](Idea)\n", "owner in the UI") }
	put, err := w.s.opWikiPut(context.Background(), owner, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: "idea\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.read("Owner.md").Content, "made in the UI") {
		t.Fatal("the owner's edit was lost")
	}
	if got := w.read("Idea.md").Content; !strings.Contains(got, "[Home](Home), [Owner](Owner)") {
		t.Fatalf("the retried put did not see the new head: %q", got)
	}
	if parent := w.git("--git-dir", w.gitDir, "rev-parse", put.Commit+"^"); !strings.Contains(w.git("--git-dir", w.gitDir, "log", "-1", "--format=%s", parent), "owner in the UI") {
		t.Fatalf("put commit's parent is %s, not the owner's edit", parent)
	}
}

// TestWikiPutConflictOnRace: the same race, but the owner changed the page
// being put: the retry finds a conflict instead of overwriting it.
func TestWikiPutConflictOnRace(t *testing.T) {
	w := newWikiRepo(t, true)
	home := w.read("Home")
	w.beforePush = func() { w.commit("Home.md", "# Home\n\nthe owner's version\n", "owner in the UI") }
	_, err := w.s.opWikiPut(context.Background(), owner, WikiPutIn{Repo: "demo", Path: "Home.md", Content: "mine\n", Base: home.SHA})
	if status(err) != http.StatusConflict {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if !strings.Contains(w.read("Home").Content, "the owner's version") {
		t.Fatal("the owner's edit was overwritten")
	}
}

// TestWikiCreate: the first write to a wiki that does not exist creates
// it, and the creation's placeholder page does not survive.
func TestWikiCreate(t *testing.T) {
	w := newWikiRepo(t, false)
	ctx := context.Background()
	list, err := w.s.opWikiList(ctx, owner, "demo")
	if err != nil || list.Head != "" || len(list.Files) != 0 {
		t.Fatalf("list of a missing wiki = %+v, %v", list, err)
	}
	if _, err := w.s.opWikiRead(ctx, owner, "demo", "Home"); status(err) != http.StatusNotFound {
		t.Fatalf("read of a missing wiki: %v", err)
	}
	gen, err := w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo"})
	if err != nil || gen.Commit != "" {
		t.Fatalf("generate on a missing wiki = %+v, %v", gen, err)
	}
	if _, err := os.Stat(w.gitDir); err == nil {
		t.Fatal("generate created the wiki")
	}
	put, err := w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "First-Idea.md", Content: "first\n"})
	if err != nil {
		t.Fatal(err)
	}
	list, err = w.s.opWikiList(ctx, owner, "demo")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range list.Files {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "First-Idea.md,Wiki-Health.md" || strings.Join(put.Changed, ",") != "First-Idea.md,Home.md,Wiki-Health.md" {
		t.Fatalf("files = %v, changed = %v", paths, put.Changed)
	}
}

// TestWikiCreateRace: the owner saves pages in the web UI between the
// wiki's creation and the first write. The write removes only the
// placeholder as created: a page the owner added stays, and so does Home
// when the owner edited it.
func TestWikiCreateRace(t *testing.T) {
	for _, editHome := range []bool{false, true} {
		t.Run(fmt.Sprintf("editHome=%v", editHome), func(t *testing.T) {
			w := newWikiRepo(t, false)
			create := w.s.wikiCreateFn
			w.s.wikiCreateFn = func(ctx context.Context, org, repo string) error {
				if err := create(ctx, org, repo); err != nil {
					return err
				}
				w.commit("Owner.md", "the owner's page\n", "owner in the UI")
				if editHome {
					w.commit("Home.md", "# Home\n\nthe owner's home\n", "owner edits Home")
				}
				return nil
			}
			ctx := context.Background()
			if _, err := w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: "idea\n"}); err != nil {
				t.Fatal(err)
			}
			if got := w.read("Owner.md").Content; !strings.Contains(got, "the owner's page") {
				t.Fatalf("Owner.md = %q", got)
			}
			_, err := w.s.opWikiRead(ctx, owner, "demo", "Home.md")
			switch {
			case editHome && err != nil:
				t.Fatalf("the owner's Home was removed: %v", err)
			case editHome && !strings.Contains(w.read("Home.md").Content, "the owner's home"):
				t.Fatalf("Home.md = %q", w.read("Home.md").Content)
			case !editHome && status(err) != http.StatusNotFound:
				t.Fatalf("the placeholder Home survived: %v", err)
			}
		})
	}
}

// TestWikiSubdirectoryBlock: a block in a file below the top level is
// filled on put, like a page's, and generate --check finds it current.
func TestWikiSubdirectoryBlock(t *testing.T) {
	w := newWikiRepo(t, true)
	ctx := context.Background()
	content := "# Round 1\n\n" + wiki.Open("health") + "\nstale\n" + wiki.Close("health") + "\n"
	if _, err := w.s.opWikiPut(ctx, owner, WikiPutIn{Repo: "demo", Path: "reviews/r1.md", Content: content}); err != nil {
		t.Fatal(err)
	}
	got := w.read("reviews/r1.md").Content
	if strings.Contains(got, "stale") || !strings.Contains(got, wiki.Open("health")+"\n- [Home](Home) (broken-link)") {
		t.Fatalf("reviews/r1.md = %q", got)
	}
	check, err := w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo", Check: true})
	if err != nil || len(check.Stale) != 0 || len(check.Changed) != 0 {
		t.Fatalf("check = %+v, %v", check, err)
	}
	// Hand-edited inside the block: check reports it, generate repairs it.
	w.commit("reviews/r1.md", strings.Replace(got, "(broken-link)", "(edited)", 1), "owner edits the block")
	check, err = w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo", Check: true})
	if err != nil || fmt.Sprint(check.Stale) != "[{reviews/r1.md health}]" {
		t.Fatalf("check after a hand edit = %+v, %v", check, err)
	}
	if gen, err := w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo"}); err != nil || strings.Join(gen.Changed, ",") != "reviews/r1.md" {
		t.Fatalf("generate = %+v, %v", gen, err)
	}
	if w.read("reviews/r1.md").Content != got {
		t.Fatal("generate did not restore the block")
	}
}

func TestWikiGenerate(t *testing.T) {
	w := newWikiRepo(t, true)
	ctx := context.Background()
	check, err := w.s.opWikiGenerate(ctx, reader, WikiGenerateIn{Repo: "demo", Check: true})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(check.Stale) != "[{Home.md backlinks} {Wiki-Health.md health}]" || len(check.Problems) != 1 || check.Problems[0].Kind != "broken-link" {
		t.Fatalf("check = %+v", check)
	}
	if _, err := w.s.opWikiGenerate(ctx, reader, WikiGenerateIn{Repo: "demo"}); status(err) != http.StatusForbidden {
		t.Fatalf("generate by an actor: %v", err)
	}
	if _, err := w.s.opWikiGenerate(ctx, claude, WikiGenerateIn{}); status(err) != http.StatusForbidden {
		t.Fatalf("generate in a run: %v", err)
	}
	gen, err := w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo"})
	if err != nil || gen.Commit == "" || strings.Join(gen.Changed, ",") != "Home.md,Wiki-Health.md" {
		t.Fatalf("generate = %+v, %v", gen, err)
	}
	if a := w.git("--git-dir", w.gitDir, "log", "-1", "--format=%an", "main"); a != "bottown" {
		t.Fatalf("generate's commit is by %s", a)
	}
	check, err = w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo", Check: true})
	if err != nil || len(check.Stale) != 0 {
		t.Fatalf("check after generate = %+v, %v", check, err)
	}
	again, err := w.s.opWikiGenerate(ctx, owner, WikiGenerateIn{Repo: "demo"})
	if err != nil || again.Commit != "" {
		t.Fatalf("second generate = %+v, %v", again, err)
	}
}

// TestWikiAccess: runs and actors read; only the owner writes, by default.
func TestWikiAccess(t *testing.T) {
	w := newWikiRepo(t, true)
	ctx := context.Background()
	for _, p := range []secrets.Principal{claude, reader} {
		if _, err := w.s.opWikiRead(ctx, p, "demo", "Home"); err != nil {
			t.Errorf("%s read: %v", p.Actor, err)
		}
		if _, err := w.s.opWikiPut(ctx, p, WikiPutIn{Repo: "demo", Path: "Idea.md", Content: "x\n"}); status(err) != http.StatusForbidden {
			t.Errorf("%s put: %v, want 403", p.Actor, err)
		}
	}
	if _, err := w.s.opWikiRead(ctx, claude, "other", "Home"); status(err) != http.StatusForbidden {
		t.Errorf("run reads another repo's wiki: %v", err)
	}
}

// TestWikiGitProxyDenied: the wiki's git repository is the owner's; an
// actor's key, in a run or not, is refused before Forgejo sees it.
func TestWikiGitProxyDenied(t *testing.T) {
	// No ForgejoURL and no tokens: a request that got past the check
	// would fail differently.
	s := &Server{Owner: "rlupi", Log: slog.Default()}
	for _, p := range []secrets.Principal{claude, reader, {Actor: "claude"}} {
		r := httptest.NewRequest("GET", "/git/bottown/demo.wiki.git/info/refs?service=git-upload-pack", nil)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, p))
		rec := httptest.NewRecorder()
		s.gitProxy(rec, r)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "bottown wiki") {
			t.Errorf("%s: %d %s", p.Actor, rec.Code, rec.Body)
		}
	}
}
