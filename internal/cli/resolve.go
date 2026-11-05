package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/runs"
)

// gitInstanceKey is the checkout-local git config key naming the instance a
// repository belongs to. `bottown repo add` sets it when it is unset.
const gitInstanceKey = "bottown.instance"

// resolveInstance picks the instance for a command run in dir when neither
// --instance nor $BOTTOWN_INSTANCE was given:
//
//  1. git config bottown.instance in the checkout;
//  2. the one configured instance that a remote URL points at;
//  3. prod.
func resolveInstance(dir string) (name, source string, err error) {
	// Repository or worktree config (an actor worktree carries its own).
	if out, err := runs.Git(dir, nil, "config", "--get", gitInstanceKey); err == nil {
		name := out
		if err := config.ValidInstance(name); err != nil {
			return "", "", fmt.Errorf("git config %s: %w", gitInstanceKey, err)
		}
		return name, "git config " + gitInstanceKey, nil
	}
	matches, err := remoteInstances(dir)
	if err != nil {
		return "", "", err
	}
	switch len(matches) {
	case 0:
		return config.DefaultInstance, "default", nil
	case 1:
		return matches[0], "git remote", nil
	default:
		return "", "", fmt.Errorf("this checkout has remotes on instances %s; pick one with `git config %s <name>` or -i",
			strings.Join(matches, ", "), gitInstanceKey)
	}
}

// remoteInstances lists the configured instances whose git endpoint some
// remote of the checkout in dir points at.
func remoteInstances(dir string) ([]string, error) {
	out, err := remoteURLs(dir)
	if err != nil {
		return nil, nil // not a checkout, or no remotes
	}
	names, err := config.ListInstances()
	if err != nil {
		return nil, err
	}
	var matches []string
	for _, n := range names {
		inst, ok, err := config.LoadInstance(n)
		if err != nil || !ok {
			continue
		}
		prefix := inst.ServerURL() + "/git/"
		for _, line := range strings.Split(out, "\n") {
			_, url, _ := strings.Cut(line, " ")
			if strings.HasPrefix(url, prefix) && !slices.Contains(matches, n) {
				matches = append(matches, n)
			}
		}
	}
	return matches, nil
}

// remoteURLs is the checkout's remote URLs, one "remote.<name>.url <url>"
// line each.
func remoteURLs(dir string) (string, error) {
	return runs.Git(dir, nil, "config", "--get-regexp", `^remote\..*\.url$`)
}

// repoOnInstance returns "org/name" if the checkout in dir has a remote on
// inst's git endpoint.
func repoOnInstance(dir string, inst config.Instance) (string, bool) {
	out, err := remoteURLs(dir)
	if err != nil {
		return "", false
	}
	prefix := inst.ServerURL() + "/git/"
	for _, line := range strings.Split(out, "\n") {
		_, url, _ := strings.Cut(line, " ")
		if rest, ok := strings.CutPrefix(url, prefix); ok {
			return strings.TrimSuffix(rest, ".git"), true
		}
	}
	return "", false
}
