package config

import (
	"bytes"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// RepoSettingsFile is the file, at the repository's root, in which a
// repository declares what bottown cannot guess. bottown reads it from the
// forge's default branch only, never from a PR branch or the checkout, so a
// change to it applies only once the owner merges it. The exception is the
// offline `bottown milestone check`, which reads the checkout's copy.
const RepoSettingsFile = ".bottown.toml"

// DefaultMilestonesDir is where milestone plan files live when
// RepoSettingsFile does not say otherwise.
const DefaultMilestonesDir = "docs/milestones"

// RepoSettings is the content of RepoSettingsFile.
type RepoSettings struct {
	// AfterPull lists the commands the owner runs in the main checkout after
	// pulling. bottown runs them only on the owner's click, shown in full.
	AfterPull []string `toml:"after_pull"`
	// MilestonesDir is the repository-relative directory holding milestone
	// plan files, <MilestonesDir>/<slug>.md.
	MilestonesDir string `toml:"milestones_dir"`
	// Check and E2E are the commands `bottown check` runs, in order, each
	// with sh -c at the checkout's root: Check always, E2E with --e2e.
	Check []string `toml:"check"`
	E2E   []string `toml:"e2e"`
}

// DefaultCheck and DefaultE2E are the commands `bottown check` runs when
// RepoSettingsFile names none.
var (
	DefaultCheck = []string{"make check"}
	DefaultE2E   = []string{"make e2e"}
)

// DefaultRepoSettings is what a repository without RepoSettingsFile gets: no
// after-pull steps, milestone plans in DefaultMilestonesDir, and the checks
// DefaultCheck and DefaultE2E.
func DefaultRepoSettings() RepoSettings {
	return RepoSettings{
		MilestonesDir: DefaultMilestonesDir,
		Check:         slices.Clone(DefaultCheck),
		E2E:           slices.Clone(DefaultE2E),
	}
}

// ParseRepoSettings decodes RepoSettingsFile. Unknown keys are an error, so
// a misspelt key is reported rather than silently ignored.
func ParseRepoSettings(b []byte) (RepoSettings, error) {
	s := DefaultRepoSettings()
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return RepoSettings{}, fmt.Errorf("%s: %w", RepoSettingsFile, err)
	}
	for _, l := range []struct {
		key  string
		cmds []string
	}{{"after_pull", s.AfterPull}, {"check", s.Check}, {"e2e", s.E2E}} {
		for i, c := range l.cmds {
			if strings.TrimSpace(c) == "" {
				return RepoSettings{}, fmt.Errorf("%s: %s[%d] is empty", RepoSettingsFile, l.key, i)
			}
		}
	}
	if err := CheckRepoPath(s.MilestonesDir); err != nil {
		return RepoSettings{}, fmt.Errorf("%s: milestones_dir %q %w", RepoSettingsFile, s.MilestonesDir, err)
	}
	return s, nil
}

// CheckRepoPath reports an error unless p is a clean path relative to the
// repository's root that stays inside it: not empty, not ".", not absolute,
// with no ".." segment and no backslash. The error completes a sentence
// naming p.
func CheckRepoPath(p string) error {
	switch {
	case p == "" || p == ".":
		return fmt.Errorf("must name a path inside the repository")
	case strings.Contains(p, `\`):
		return fmt.Errorf("must use / as the separator")
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("must be relative to the repository's root")
	case path.Clean(p) != p:
		return fmt.Errorf("must be a clean path (%q)", path.Clean(p))
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("must not contain ..")
		}
	}
	return nil
}
