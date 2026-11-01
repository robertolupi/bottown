package config

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// RepoSettingsFile is the file, at the repository's root, in which a
// repository declares what bottown cannot guess. bottown reads it from the
// forge's default branch only, never from a PR branch or the checkout, so a
// change to it applies only once the owner merges it.
const RepoSettingsFile = ".bottown.toml"

// RepoSettings is the content of RepoSettingsFile.
type RepoSettings struct {
	// AfterPull lists the commands the owner runs in the main checkout after
	// pulling. bottown runs them only on the owner's click, shown in full.
	AfterPull []string `toml:"after_pull"`
}

// DefaultRepoSettings is what a repository without RepoSettingsFile gets: no
// after-pull steps.
func DefaultRepoSettings() RepoSettings { return RepoSettings{} }

// ParseRepoSettings decodes RepoSettingsFile. Unknown keys are an error, so
// a misspelt key is reported rather than silently ignored.
func ParseRepoSettings(b []byte) (RepoSettings, error) {
	s := DefaultRepoSettings()
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return RepoSettings{}, fmt.Errorf("%s: %w", RepoSettingsFile, err)
	}
	for i, c := range s.AfterPull {
		if strings.TrimSpace(c) == "" {
			return RepoSettings{}, fmt.Errorf("%s: after_pull[%d] is empty", RepoSettingsFile, i)
		}
	}
	return s, nil
}
