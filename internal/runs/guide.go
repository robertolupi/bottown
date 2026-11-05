package runs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The fam guide: what an agent needs to know to work in a fam, whatever the
// project (the layout, the remotes, how runs start and what is allowed
// outside one). The project's own instructions stay in its AGENTS.md. The
// guide is rendered from prompts/fam.md, printed by `bottown guide`, and
// written into each actor worktree as an untracked file in the slot its
// harness loads by itself, next to the project's AGENTS.md, never instead
// of it.

// GuideMarker identifies guide files bottown wrote, so it refreshes them and
// never overwrites a file someone else wrote.
const GuideMarker = "written by bottown from `bottown guide`; refreshed by `bottown actor setup` and `bottown run start`"

// GuideData fills the fam guide.
type GuideData struct {
	Repo    string // org/name on the forge, if known
	Main    string // the owner's checkout
	Actors  []GuideActor
	Actor   string // this worktree's actor, if it is an actor worktree
	Remote  string // the bottown remote
	Bottown string // absolute path of the bottown CLI
	Marker  string // set by Guide
}

// GuideActor is one actor worktree of the fam.
type GuideActor struct{ Name, Path string }

// Guide renders the fam guide.
func Guide(d GuideData) (string, error) {
	d.Marker = GuideMarker
	var b bytes.Buffer
	if err := prompts.ExecuteTemplate(&b, "fam.md", d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// guideFiles are, per harness, the worktree file that harness loads by
// itself as instructions in addition to the project's AGENTS.md. Codex has
// none: AGENTS.override.md replaces AGENTS.md in its directory, so codex
// finds the guide through the bottown skill (`bottown harness setup`).
var guideFiles = map[string]string{
	"claude":      "CLAUDE.local.md",
	"crush":       "CRUSH.local.md", // crush also loads CLAUDE.local.md: one of the two only
	"antigravity": ".agents/rules/bottown.md",
	"agy":         ".agents/rules/bottown.md",
	"kimi":        ".kimi-code/AGENTS.md",
}

// GuideFile is the worktree-relative path of harness's guide file, or ""
// when the harness has no slot for it.
func GuideFile(harness string) string { return guideFiles[harness] }

// GuideFiles are every slot's path, sorted, without duplicates.
func GuideFiles() []string {
	var out []string
	for _, p := range guideFiles {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// Guide states of an actor worktree (GuideStatus).
const (
	GuideCurrent  = "current"  // the file holds the current guide
	GuideOutdated = "outdated" // bottown wrote it, but the guide has changed
	GuideMissing  = "missing"  // not written yet
	GuideForeign  = "foreign"  // a file bottown did not write is in the slot
	GuideNone     = "none"     // the harness has no slot (codex: the skill)
)

// GuideStatus reports the state of the guide file for harness in the
// worktree dir against the rendered guide want.
func GuideStatus(dir, harness, want string) string {
	rel := GuideFile(harness)
	if rel == "" {
		return GuideNone
	}
	if tracked(dir, rel) {
		return GuideForeign
	}
	b, err := os.ReadFile(filepath.Join(dir, rel))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return GuideMissing
	case err != nil, !bytes.Contains(b, []byte(GuideMarker)):
		return GuideForeign
	case string(b) == want:
		return GuideCurrent
	}
	return GuideOutdated
}

// WriteGuide writes the guide for harness into the actor worktree dir,
// excluded from git for every worktree of the checkout, and removes guide
// files bottown wrote in other harnesses' slots (an actor whose harness
// changed). It returns the file it wrote, or "" with a reason when it
// wrote none: the harness has no slot, or the slot holds a file bottown did
// not write, which it leaves alone.
func WriteGuide(dir, harness, guide string) (path, skipped string, err error) {
	if err := excludeGuides(dir); err != nil {
		return "", "", err
	}
	rel := GuideFile(harness)
	for _, other := range GuideFiles() {
		if other != rel {
			removeOwnGuide(dir, other)
		}
	}
	if rel == "" {
		return "", fmt.Sprintf("harness %s loads no per-worktree instructions; it finds the guide through the bottown skill (`bottown harness setup`)", harness), nil
	}
	switch GuideStatus(dir, harness, guide) {
	case GuideCurrent:
		return rel, "", nil
	case GuideForeign:
		return "", fmt.Sprintf("%s exists and was not written by bottown; it is left alone", rel), nil
	}
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(p, []byte(guide), 0o644); err != nil {
		return "", "", err
	}
	return rel, "", nil
}

// removeOwnGuide removes the untracked file rel in dir if bottown wrote it.
func removeOwnGuide(dir, rel string) {
	p := filepath.Join(dir, rel)
	if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(GuideMarker)) && !tracked(dir, rel) {
		_ = os.Remove(p)
	}
}

func tracked(dir, rel string) bool {
	out, err := Git(dir, nil, "ls-files", "--", rel)
	return err == nil && out != ""
}

// excludeGuides lists every guide slot in the checkout's shared
// info/exclude, so no worktree of it shows or commits a guide file.
func excludeGuides(dir string) error {
	p, err := Git(dir, nil, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	for _, rel := range GuideFiles() {
		if err := appendLine(p, "/"+strings.TrimPrefix(rel, "/")); err != nil {
			return err
		}
	}
	return nil
}
