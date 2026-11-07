package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/wiki"
)

// The wiki store: every read and write of a repository's wiki goes through
// here (agents get no wiki checkout; the proxies deny Forgejo's wiki API and
// the wiki's git repository to everyone but the owner). It reads Forgejo's
// own bare repository of the wiki, so it is never behind, and writes by
// committing there and pushing to Forgejo as the writer, so Forgejo's hooks
// and page history see an ordinary push.
//
// A put is a compare-and-swap: it names the blob it read (--base), and
// fails with a conflict when the page's text has changed since. Changes to
// generated blocks alone are no conflict. A push that loses a race (the
// owner editing in the web UI) is rebuilt on the new head and retried. A
// delete is the same compare-and-swap, and regenerates the wiki's blocks in
// its commit.
//
//	GET    /v1/wiki/files?repo=
//	GET    /v1/wiki/file?repo=&path=        (path: a file, or a page's slug)
//	PUT    /v1/wiki/file  {repo, path, content, base, message}
//	DELETE /v1/wiki/file  {repo, path, base, message}
//	POST   /v1/wiki/generate {repo, check}

// ErrorCodeWikiConflict is a put refused because the page changed since
// the writer read it.
const ErrorCodeWikiConflict = "wiki_conflict"

// wikiAttempts bounds the retries of a push that lost a race.
const wikiAttempts = 5

// WikiFile is one file of a wiki.
type WikiFile struct {
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Page    bool   `json:"page"`               // a top-level .md file: a page Forgejo renders
	WebPath string `json:"web_path,omitempty"` // for a page: its URL below /wiki/
}

// WikiListOut is a wiki's files at its head.
type WikiListOut struct {
	Repo  string     `json:"repo"`
	Head  string     `json:"head,omitempty"` // empty: the wiki has no pages yet
	Files []WikiFile `json:"files"`
}

// WikiReadOut is one file and the blob a put names as its base.
type WikiReadOut struct {
	Repo    string `json:"repo"`
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Head    string `json:"head"`
	Content string `json:"content"`
}

// WikiPutIn writes one file. Base is the blob SHA the writer read; empty
// creates the file, and fails if it exists.
type WikiPutIn struct {
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Base    string `json:"base,omitempty"`
	Message string `json:"message,omitempty"`
}

// WikiPutOut is the commit a put made, if any.
type WikiPutOut struct {
	Repo      string   `json:"repo"`
	Path      string   `json:"path"`
	SHA       string   `json:"sha"`
	Commit    string   `json:"commit,omitempty"`
	Changed   []string `json:"changed"` // every file the commit changed, generated blocks included
	Unchanged bool     `json:"unchanged,omitempty"`
}

// WikiGenerateIn regenerates a wiki's blocks; Check only reports.
type WikiGenerateIn struct {
	Repo  string `json:"repo,omitempty"`
	Check bool   `json:"check,omitempty"`
}

// WikiGenerateOut is what generate changed, or with check would change.
type WikiGenerateOut struct {
	Repo     string         `json:"repo"`
	Commit   string         `json:"commit,omitempty"`
	Changed  []string       `json:"changed"`
	Stale    []wiki.Stale   `json:"stale"`
	Problems []wiki.Problem `json:"problems"`
}

func (s *Server) wikiRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/wiki/files", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.opWikiList(r.Context(), Who(r.Context()), r.URL.Query().Get("repo"))
		writeOp(w, out, err)
	})))
	mux.Handle("GET /v1/wiki/file", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, err := s.opWikiRead(r.Context(), Who(r.Context()), r.URL.Query().Get("repo"), r.URL.Query().Get("path"))
		writeOp(w, out, err)
	})))
	mux.Handle("PUT /v1/wiki/file", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in WikiPutIn
		if !decode(w, r, &in) {
			return
		}
		out, err := s.opWikiPut(r.Context(), Who(r.Context()), in)
		var ce *wikiConflict
		if errors.As(err, &ce) {
			writeErrorCode(w, http.StatusConflict, ErrorCodeWikiConflict, err.Error())
			return
		}
		writeOp(w, out, err)
	})))
	mux.Handle("DELETE /v1/wiki/file", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in WikiRmIn
		if !decode(w, r, &in) {
			return
		}
		out, err := s.opWikiRm(r.Context(), Who(r.Context()), in)
		var ce *wikiConflict
		if errors.As(err, &ce) {
			writeErrorCode(w, http.StatusConflict, ErrorCodeWikiConflict, err.Error())
			return
		}
		writeOp(w, out, err)
	})))
	mux.Handle("POST /v1/wiki/generate", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in WikiGenerateIn
		if !decode(w, r, &in) {
			return
		}
		out, err := s.opWikiGenerate(r.Context(), Who(r.Context()), in)
		writeOp(w, out, err)
	})))
}

type wikiConflict struct{ msg string }

func (e *wikiConflict) Error() string { return e.msg }

// wikiWho is the principal as the wiki rules name it.
func (s *Server) wikiWho(p secrets.Principal) string {
	switch {
	case p.Run != nil:
		return p.Run.Kind
	case p.Actor == s.Owner:
		return policy.WikiOwner
	}
	return policy.WikiActor
}

// wikiAllow applies the wiki rules to one file, auditing refusals.
func (s *Server) wikiAllow(p secrets.Principal, repo, op string, write bool, file string) error {
	d := policy.DecideWiki(s.wikiWho(p), write, file)
	if d.Allow && write {
		if err := requireWrite(p); err != nil {
			d = policy.Decision{Rule: "read-only-key"}
		}
	}
	if !d.Allow {
		s.record(AuditEntry{Actor: p.Actor, Run: runID(p), Repo: repo, Op: op, Path: file, Decision: "deny", Rule: d.Rule})
		verb := "read"
		if write {
			verb = "write"
		}
		return ferr(http.StatusForbidden, "may not %s wiki file %s (bottown policy rule %s)", verb, file, d.Rule)
	}
	return nil
}

// cleanWikiPath checks a path inside the wiki: relative, clean, no dot
// segments or git internals.
func cleanWikiPath(p string) (string, error) {
	if p == "" {
		return "", ferr(http.StatusBadRequest, "path is required")
	}
	if strings.ContainsAny(p, "\\\x00\n\r") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "", ferr(http.StatusBadRequest, "wiki path %q must be relative and clean", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", ferr(http.StatusBadRequest, "wiki path %q has a segment starting with a dot", p)
		}
	}
	return p, nil
}

// wikiState is a wiki at one head.
type wikiState struct {
	gitDir string
	branch string
	head   string            // "" when the wiki does not exist yet
	tree   wiki.Tree         // path -> content
	blobs  map[string]string // path -> blob SHA
	// placeholder: when this write created the wiki, the files of the
	// creation commit still exactly as it made them. The write removes
	// them; anything changed or added since (the owner editing in the web
	// UI before the write landed) stays.
	placeholder map[string]bool
}

func (st *wikiState) files() []string { return st.tree.Files() }

func (s *Server) wikiGitDir(org, repo string) string {
	return s.repoGitDir(org, repo+".wiki")
}

// readWiki reads a wiki's head and every file in it.
func (s *Server) readWiki(ctx context.Context, org, repo string) (*wikiState, error) {
	if s.GitRoot == "" {
		return nil, ferr(http.StatusNotImplemented, "forge git repositories are not available")
	}
	st := &wikiState{gitDir: s.wikiGitDir(org, repo), tree: wiki.Tree{}, blobs: map[string]string{}}
	if _, err := os.Stat(st.gitDir); errors.Is(err, os.ErrNotExist) {
		return st, nil
	} else if err != nil {
		return nil, err
	}
	ref, err := mergeGit(ctx, st.gitDir, nil, nil, "symbolic-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	st.branch = strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	head, err := mergeGit(ctx, st.gitDir, nil, nil, "rev-parse", "--verify", "-q", "refs/heads/"+st.branch+"^{commit}")
	if err != nil {
		return st, nil // an empty wiki repository: no commits yet
	}
	st.head = strings.TrimSpace(head)
	if st.blobs, err = lsTree(ctx, st.gitDir, st.head); err != nil {
		return nil, err
	}
	paths := slices.Sorted(maps.Keys(st.blobs))
	shas := make([]string, len(paths))
	for i, p := range paths {
		shas[i] = st.blobs[p]
	}
	contents, err := catBlobs(ctx, st.gitDir, shas)
	if err != nil {
		return nil, err
	}
	for i, p := range paths {
		st.tree[p] = contents[i]
	}
	return st, nil
}

// lsTree lists a commit's files: path -> blob SHA.
func lsTree(ctx context.Context, gitDir, commit string) (map[string]string, error) {
	ls, err := mergeGit(ctx, gitDir, nil, nil, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}
	blobs := map[string]string{}
	for _, ent := range strings.Split(strings.TrimSuffix(ls, "\x00"), "\x00") {
		meta, p, ok := strings.Cut(ent, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[1] != "blob" {
			continue
		}
		blobs[p] = f[2]
	}
	return blobs, nil
}

// catBlobs reads blobs in one git process.
func catBlobs(ctx context.Context, gitDir string, shas []string) ([][]byte, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	cmd := runs.GitCommand(ctx, "", []string{"--git-dir", gitDir}, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	r := bufio.NewReader(&stdout)
	out := make([][]byte, 0, len(shas))
	for range shas {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("git cat-file: %w", err)
		}
		f := strings.Fields(hdr)
		if len(f) != 3 {
			return nil, fmt.Errorf("git cat-file: %s", strings.TrimSpace(hdr))
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, err
		}
		b := make([]byte, n+1) // the content and its trailing newline
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, fmt.Errorf("git cat-file: %w", err)
		}
		out = append(out, b[:n])
	}
	return out, nil
}

// blob reads one blob by SHA; ok is false when the repository lacks it.
func blob(ctx context.Context, gitDir, sha string) ([]byte, bool) {
	if _, err := mergeGit(ctx, gitDir, nil, nil, "cat-file", "-e", sha+"^{blob}"); err != nil {
		return nil, false
	}
	bs, err := catBlobs(ctx, gitDir, []string{sha})
	if err != nil {
		return nil, false
	}
	return bs[0], true
}

func (s *Server) wikiOptions(org, repo string) wiki.Options {
	return wiki.Options{Base: "/" + org + "/" + repo + "/wiki"}
}

func (s *Server) opWikiList(ctx context.Context, p secrets.Principal, repoName string) (WikiListOut, error) {
	org, repo, err := s.resolveRepo(p, repoName)
	if err != nil {
		return WikiListOut{}, err
	}
	st, err := s.readWiki(ctx, org, repo)
	if err != nil {
		return WikiListOut{}, err
	}
	out := WikiListOut{Repo: org + "/" + repo, Head: st.head, Files: []WikiFile{}}
	for _, f := range st.files() {
		if policy.DecideWiki(s.wikiWho(p), false, f).Allow {
			wf := WikiFile{Path: f, SHA: st.blobs[f], Page: wiki.IsPage(f)}
			if wf.Page {
				wf.WebPath = wiki.WebPath(f)
			}
			out.Files = append(out.Files, wf)
		}
	}
	return out, nil
}

func (s *Server) opWikiRead(ctx context.Context, p secrets.Principal, repoName, file string) (WikiReadOut, error) {
	org, repo, err := s.resolveRepo(p, repoName)
	if err != nil {
		return WikiReadOut{}, err
	}
	if file, err = cleanWikiPath(file); err != nil {
		return WikiReadOut{}, err
	}
	st, err := s.readWiki(ctx, org, repo)
	if err != nil {
		return WikiReadOut{}, err
	}
	f, ok := wiki.Resolve(st.files(), file)
	if err := s.wikiAllow(p, org+"/"+repo, "wiki_read", false, f); err != nil {
		return WikiReadOut{}, err
	}
	if !ok {
		return WikiReadOut{}, ferr(http.StatusNotFound, "wiki file %s not found in %s/%s", file, org, repo)
	}
	return WikiReadOut{Repo: org + "/" + repo, Path: f, SHA: st.blobs[f], Head: st.head, Content: string(st.tree[f])}, nil
}

// wikiChange is a commit to make on a wiki: files to write, and files to
// remove (the creation's placeholder, on the wiki's first write).
type wikiChange struct {
	files   map[string][]byte
	remove  []string
	msg     string
	bottown bool // authored by bottown (regeneration), not the writer
}

func (s *Server) opWikiPut(ctx context.Context, p secrets.Principal, in WikiPutIn) (WikiPutOut, error) {
	org, repo, err := s.resolveRepo(p, in.Repo)
	if err != nil {
		return WikiPutOut{}, err
	}
	full := org + "/" + repo
	file, err := cleanWikiPath(in.Path)
	if err != nil {
		return WikiPutOut{}, err
	}
	if err := requireWrite(p); err != nil {
		return WikiPutOut{}, err
	}
	content, err := wiki.Normalize(in.Content)
	if err != nil {
		return WikiPutOut{}, ferr(http.StatusBadRequest, "%s: %v", file, err)
	}
	out := WikiPutOut{Repo: full}
	var final []byte // the file as committed, its blocks filled
	commit, changed, err := s.wikiCommit(ctx, p, org, repo, true, func(st *wikiState) (*wikiChange, error) {
		// The wiki as it is once the placeholder is gone: a placeholder
		// file does not exist for the put.
		tree := maps.Clone(st.tree)
		for f := range st.placeholder {
			delete(tree, f)
		}
		f, exists := wiki.Resolve(tree.Files(), file)
		if !strings.HasSuffix(f, ".md") {
			return nil, ferr(http.StatusBadRequest, "wiki files are markdown: %s does not end in .md", f)
		}
		if err := s.wikiAllow(p, full, "wiki_put", true, f); err != nil {
			return nil, err
		}
		out.Path = f
		cur := st.blobs[f]
		switch {
		case in.Base == "" && exists:
			return nil, &wikiConflict{fmt.Sprintf("%s exists (blob %s): read it and put with --base", f, cur)}
		case in.Base != "" && !exists:
			return nil, &wikiConflict{fmt.Sprintf("%s no longer exists in the wiki", f)}
		case in.Base != "" && in.Base != cur:
			// A change to generated blocks alone is not a conflict.
			old, ok := blob(ctx, st.gitDir, in.Base)
			if !ok || !sameText(string(old), string(st.tree[f])) {
				return nil, &wikiConflict{fmt.Sprintf("%s changed since blob %s (now %s): read it again and re-apply your edit", f, shortCommit(in.Base), shortCommit(cur))}
			}
		}
		tree[f] = []byte(content)
		files := map[string][]byte{f: []byte(content)}
		for gp, b := range wiki.Generate(tree, s.wikiOptions(org, repo)).Changed {
			files[gp] = b
		}
		final = files[f]
		var remove []string
		for pf := range st.placeholder {
			if _, ok := files[pf]; !ok {
				remove = append(remove, pf)
			}
		}
		slices.Sort(remove)
		msg := in.Message
		if msg == "" {
			msg = "Update " + wiki.WebPath(f)
		}
		return &wikiChange{files: files, remove: remove, msg: msg}, nil
	})
	if err != nil {
		var ce *wikiConflict
		if errors.As(err, &ce) {
			s.toolAudit(p, full, "wiki_put", "deny", err.Error())
		}
		return WikiPutOut{}, err
	}
	out.Commit, out.Changed, out.Unchanged = commit, changed, commit == ""
	if out.SHA, err = hashBlob(ctx, s.wikiGitDir(org, repo), final); err != nil {
		return WikiPutOut{}, err
	}
	if commit != "" {
		s.toolAudit(p, full, "wiki_put", "allow", fmt.Sprintf("%s at %s", out.Path, shortCommit(commit)))
	}
	return out, nil
}

// WikiRmIn deletes one file. Base is the blob SHA the writer read; it is
// required.
type WikiRmIn struct {
	Repo    string `json:"repo,omitempty"`
	Path    string `json:"path"`
	Base    string `json:"base"`
	Message string `json:"message,omitempty"`
}

// WikiRmOut is the commit a delete made.
type WikiRmOut struct {
	Repo    string   `json:"repo"`
	Path    string   `json:"path"`
	Commit  string   `json:"commit"`
	Changed []string `json:"changed"` // every file the commit changed: the deleted one, and regenerated blocks
}

// opWikiRm deletes a file, a compare-and-swap like a put, and regenerates
// the wiki's blocks in the same commit.
func (s *Server) opWikiRm(ctx context.Context, p secrets.Principal, in WikiRmIn) (WikiRmOut, error) {
	org, repo, err := s.resolveRepo(p, in.Repo)
	if err != nil {
		return WikiRmOut{}, err
	}
	full := org + "/" + repo
	file, err := cleanWikiPath(in.Path)
	if err != nil {
		return WikiRmOut{}, err
	}
	if in.Base == "" {
		return WikiRmOut{}, ferr(http.StatusBadRequest, "base is required: the blob SHA wiki read printed")
	}
	if err := requireWrite(p); err != nil {
		return WikiRmOut{}, err
	}
	out := WikiRmOut{Repo: full}
	commit, changed, err := s.wikiCommit(ctx, p, org, repo, false, func(st *wikiState) (*wikiChange, error) {
		f, exists := wiki.Resolve(st.files(), file)
		if err := s.wikiAllow(p, full, "wiki_rm", true, f); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ferr(http.StatusNotFound, "wiki file %s not found in %s", file, full)
		}
		if f == wiki.HealthPage {
			return nil, ferr(http.StatusBadRequest, "%s is bottown's: the generator recreates it", f)
		}
		out.Path = f
		if cur := st.blobs[f]; in.Base != cur {
			// A change to generated blocks alone is not a conflict.
			old, ok := blob(ctx, st.gitDir, in.Base)
			if !ok || !sameText(string(old), string(st.tree[f])) {
				return nil, &wikiConflict{fmt.Sprintf("%s changed since blob %s (now %s): read it again before deleting it", f, shortCommit(in.Base), shortCommit(cur))}
			}
		}
		tree := maps.Clone(st.tree)
		delete(tree, f)
		files := wiki.Generate(tree, s.wikiOptions(org, repo)).Changed
		msg := in.Message
		if msg == "" {
			msg = "Delete " + wiki.WebPath(f)
		}
		return &wikiChange{files: files, remove: []string{f}, msg: msg}, nil
	})
	if err != nil {
		var ce *wikiConflict
		if errors.As(err, &ce) {
			s.toolAudit(p, full, "wiki_rm", "deny", err.Error())
		}
		return WikiRmOut{}, err
	}
	if commit == "" {
		// wikiCommit asks nothing of a wiki that does not exist yet.
		return WikiRmOut{}, ferr(http.StatusNotFound, "wiki file %s not found in %s: the wiki has no pages yet", file, full)
	}
	out.Commit, out.Changed = commit, changed
	s.toolAudit(p, full, "wiki_rm", "allow", fmt.Sprintf("%s at %s", out.Path, shortCommit(commit)))
	return out, nil
}

// sameText reports whether two versions of a page differ only in their
// generated blocks.
func sameText(a, b string) bool {
	na, err1 := wiki.Normalize(a)
	nb, err2 := wiki.Normalize(b)
	return err1 == nil && err2 == nil && na == nb
}

// freshMessage marks the commit with which bottown creates a wiki through
// Forgejo's API: the store's first write removes its placeholder.
const freshMessage = "Create the wiki (bottown)"

func (s *Server) opWikiGenerate(ctx context.Context, p secrets.Principal, in WikiGenerateIn) (WikiGenerateOut, error) {
	org, repo, err := s.resolveRepo(p, in.Repo)
	if err != nil {
		return WikiGenerateOut{}, err
	}
	full := org + "/" + repo
	out := WikiGenerateOut{Repo: full, Changed: []string{}, Stale: []wiki.Stale{}, Problems: []wiki.Problem{}}
	if in.Check {
		st, err := s.readWiki(ctx, org, repo)
		if err != nil {
			return out, err
		}
		if err := s.wikiAllow(p, full, "wiki_generate", false, "/"); err != nil {
			return out, err
		}
		if st.head == "" {
			return out, nil
		}
		res := wiki.Generate(st.tree, s.wikiOptions(org, repo))
		out.Changed, out.Stale, out.Problems = sortedKeys(res.Changed), nonNil(res.Stale), nonNilP(res.Problems)
		return out, nil
	}
	if p.Actor != s.Owner || p.Run != nil {
		s.toolAudit(p, full, "wiki_generate", "deny", "owner only")
		return out, ferr(http.StatusForbidden, "only the owner regenerates the wiki; puts regenerate it themselves")
	}
	var res wiki.Result
	commit, changed, err := s.wikiCommit(ctx, p, org, repo, false, func(st *wikiState) (*wikiChange, error) {
		res = wiki.Generate(st.tree, s.wikiOptions(org, repo))
		if len(res.Changed) == 0 {
			return nil, nil
		}
		return &wikiChange{files: res.Changed, msg: "Regenerate the wiki's generated blocks", bottown: true}, nil
	})
	if err != nil {
		return out, err
	}
	out.Commit, out.Changed, out.Stale, out.Problems = commit, changed, nonNil(res.Stale), nonNilP(res.Problems)
	if commit != "" {
		s.toolAudit(p, full, "wiki_generate", "allow", fmt.Sprintf("%d files at %s", len(changed), shortCommit(commit)))
	}
	return out, nil
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func nonNil(s []wiki.Stale) []wiki.Stale {
	if s == nil {
		return []wiki.Stale{}
	}
	return s
}

func nonNilP(s []wiki.Problem) []wiki.Problem {
	if s == nil {
		return []wiki.Problem{}
	}
	return s
}

// wikiCommit reads the wiki, asks change what to commit on it, commits and
// pushes, and starts over from the new head when the push lost a race. A
// nil change, or one that changes no file, commits nothing. create says
// the change may create the wiki when it does not exist yet.
func (s *Server) wikiCommit(ctx context.Context, p secrets.Principal, org, repo string, create bool, change func(*wikiState) (*wikiChange, error)) (string, []string, error) {
	s.wikiMu.Lock()
	defer s.wikiMu.Unlock()
	var placeholder map[string]string // the creation commit's files, when this write created the wiki
	for range wikiAttempts {
		st, err := s.readWiki(ctx, org, repo)
		if err != nil {
			return "", nil, err
		}
		for f, sha := range placeholder {
			if st.blobs[f] == sha {
				if st.placeholder == nil {
					st.placeholder = map[string]bool{}
				}
				st.placeholder[f] = true
			}
		}
		if st.head == "" {
			if !create {
				return "", []string{}, nil
			}
			// Check the change before creating the wiki for it.
			if _, err := change(st); err != nil {
				return "", nil, err
			}
			if placeholder, err = s.createWiki(ctx, p, org, repo); err != nil {
				return "", nil, err
			}
			continue
		}
		c, err := change(st)
		if err != nil {
			return "", nil, err
		}
		if c == nil {
			return "", []string{}, nil
		}
		var changed []string
		for f, b := range c.files {
			if old, ok := st.tree[f]; !ok || !bytes.Equal(old, b) {
				changed = append(changed, f)
			}
		}
		for _, f := range c.remove {
			if _, ok := st.tree[f]; ok {
				changed = append(changed, f)
			}
		}
		slices.Sort(changed)
		if len(changed) == 0 {
			return "", []string{}, nil
		}
		commit, err := s.writeWikiCommit(ctx, p, st, c)
		if err != nil {
			return "", nil, err
		}
		err = s.wikiPush(ctx, p, org, repo, st, commit)
		if err == nil {
			return commit, changed, nil
		}
		if now, rerr := s.readWiki(ctx, org, repo); rerr == nil && now.head != st.head {
			s.Log.Info("wiki push lost a race; retrying on the new head", "repo", org+"/"+repo, "head", now.head)
			continue
		}
		return "", nil, err
	}
	return "", nil, ferr(http.StatusConflict, "the wiki kept moving; %d attempts lost the race", wikiAttempts)
}

// writeWikiCommit makes the commit for a change on st's head, in the wiki's
// repository, without a worktree.
func (s *Server) writeWikiCommit(ctx context.Context, p secrets.Principal, st *wikiState, c *wikiChange) (string, error) {
	tmp, err := os.MkdirTemp("", "bottown-wiki-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	idx := []string{"GIT_INDEX_FILE=" + tmp + "/index"}
	if _, err := mergeGit(ctx, st.gitDir, nil, idx, "read-tree", st.head); err != nil {
		return "", err
	}
	if len(c.remove) > 0 {
		// Mode 0 removes an entry; unlike --force-remove it needs no work tree.
		var rm strings.Builder
		for _, f := range c.remove {
			fmt.Fprintf(&rm, "0 %s\t%s\x00", strings.Repeat("0", len(st.head)), f)
		}
		if _, err := mergeGit(ctx, st.gitDir, strings.NewReader(rm.String()), idx, "update-index", "-z", "--index-info"); err != nil {
			return "", err
		}
	}
	for _, f := range sortedKeys(c.files) {
		sha, err := hashBlob(ctx, st.gitDir, c.files[f])
		if err != nil {
			return "", err
		}
		if _, err := mergeGit(ctx, st.gitDir, nil, idx, "update-index", "--add", "--cacheinfo", "100644,"+sha+","+f); err != nil {
			return "", err
		}
	}
	tree, err := mergeGit(ctx, st.gitDir, nil, idx, "write-tree")
	if err != nil {
		return "", err
	}
	name, email := p.Actor, p.Actor+"@bottown.local"
	msg := c.msg + "\n"
	if p.Run != nil {
		msg += "\nBottown-Run: " + p.Run.ID + "\n"
	}
	if c.bottown {
		name, email = "bottown", "bottown@localhost"
	}
	commit, err := mergeGit(ctx, st.gitDir, strings.NewReader(msg), []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=bottown", "GIT_COMMITTER_EMAIL=bottown@localhost",
	}, "commit-tree", strings.TrimSpace(tree), "-p", st.head)
	return strings.TrimSpace(commit), err
}

// hashBlob writes b to the repository as a blob and returns its SHA.
func hashBlob(ctx context.Context, gitDir string, b []byte) (string, error) {
	sha, err := mergeGit(ctx, gitDir, strings.NewReader(string(b)), nil, "hash-object", "-w", "--stdin")
	return strings.TrimSpace(sha), err
}

// wikiPush pushes commit to the wiki's branch on Forgejo, as the writer,
// without force: a push that lost a race is refused.
func (s *Server) wikiPush(ctx context.Context, p secrets.Principal, org, repo string, st *wikiState, commit string) error {
	if s.wikiPushFn != nil {
		return s.wikiPushFn(ctx, st.gitDir, st.branch, st.head, commit)
	}
	token, ok := s.actorToken(p.Actor)
	if !ok {
		return ferr(http.StatusForbidden, "no forge token for %s", p.Actor)
	}
	remote := strings.TrimRight(s.Rec.BaseURL, "/") + "/" + org + "/" + repo + ".wiki.git"
	auth := base64.StdEncoding.EncodeToString([]byte(p.Actor + ":" + token))
	_, err := mergeGit(ctx, st.gitDir, nil, []string{
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + auth,
	}, "push", remote, commit+":refs/heads/"+st.branch)
	if err != nil {
		return fmt.Errorf("push to the wiki: %w", err)
	}
	return nil
}

// createWiki creates a wiki that does not exist yet, through Forgejo's API
// as the writer, and returns the creation commit's files (path -> blob),
// the placeholder the first write removes. The head may already be past
// the creation (the owner saving a page in between), so the creation
// commit is found as the history's root, and only when its message is
// bottown's: a wiki someone else created at the same moment has no
// placeholder of bottown's to remove.
func (s *Server) createWiki(ctx context.Context, p secrets.Principal, org, repo string) (map[string]string, error) {
	if s.wikiCreateFn != nil {
		if err := s.wikiCreateFn(ctx, org, repo); err != nil {
			return nil, err
		}
	} else {
		c, err := s.actorClient(ctx, p.Actor)
		if err != nil {
			return nil, err
		}
		if err := c.CreateWikiPage(org, repo, "Home", "This wiki is kept through bottown.\n", freshMessage); err != nil {
			return nil, fmt.Errorf("create the wiki of %s/%s: %w", org, repo, err)
		}
	}
	st, err := s.readWiki(ctx, org, repo)
	if err != nil {
		return nil, err
	}
	if st.head == "" {
		return nil, fmt.Errorf("created the wiki of %s/%s, but it has no commit", org, repo)
	}
	roots, err := mergeGit(ctx, st.gitDir, nil, nil, "rev-list", "--max-parents=0", st.head)
	if err != nil {
		return nil, err
	}
	if r := strings.Fields(roots); len(r) == 1 {
		subj, err := mergeGit(ctx, st.gitDir, nil, nil, "log", "-1", "--format=%s", r[0])
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(subj) == freshMessage {
			return lsTree(ctx, st.gitDir, r[0])
		}
	}
	s.Log.Info("the new wiki's first commit is not bottown's; keeping its files", "repo", org+"/"+repo)
	return map[string]string{}, nil
}
