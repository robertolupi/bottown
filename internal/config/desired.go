// Package config holds bottown's two configuration documents: the desired
// forge state that the in-container reconciler converges to, and the host-side
// per-instance config that `bottown up` writes under ~/.bottown.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

//go:embed default.toml
var defaultDesired []byte

// Desired is the forge state bottown reconciles on every start.
type Desired struct {
	Org        string     `toml:"org"`
	Teams      []Team     `toml:"teams"`
	Bots       []Bot      `toml:"bots"`
	Labels     []Label    `toml:"labels"`
	Protection Protection `toml:"protection"`
	OwnerScope []string   `toml:"owner_token_scopes"`
}

// Team is an organization team covering all repositories.
type Team struct {
	Name  string            `toml:"name"`
	Units map[string]string `toml:"units"` // unit -> read|write
}

// Bot is a token-only forge account driven by bottown.
type Bot struct {
	Name     string   `toml:"name"`
	FullName string   `toml:"full_name"`
	Team     string   `toml:"team"`
	Scopes   []string `toml:"token_scopes"`
	// Keys reports whether host clients may be issued API keys for this bot.
	// The manager acts only inside bottown, so it never gets one.
	Keys bool `toml:"api_keys"`
}

// Label is an organization-wide label.
type Label struct {
	Name        string `toml:"name"`
	Color       string `toml:"color"`
	Description string `toml:"description"`
	Exclusive   bool   `toml:"exclusive"`
}

// Protection is applied to each repository's default branch. The owner is
// always added to the push, merge and approval allow-lists.
type Protection struct {
	RequiredApprovals      int64    `toml:"required_approvals"`
	ApprovalsWhitelist     []string `toml:"approvals_whitelist"`
	MergeWhitelist         []string `toml:"merge_whitelist"`
	DismissStaleApprovals  bool     `toml:"dismiss_stale_approvals"`
	BlockOnRejectedReviews bool     `toml:"block_on_rejected_reviews"`
	ApplyToAdmins          bool     `toml:"apply_to_admins"`
}

// DesiredPath is where an operator can mount an override inside the container.
const DesiredPath = "/data/bottown/bottown.toml"

// LoadDesired reads path if it exists, otherwise the embedded default.
func LoadDesired(path string) (Desired, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b = defaultDesired
	} else if err != nil {
		return Desired{}, err
	}
	var d Desired
	if err := toml.Unmarshal(b, &d); err != nil {
		return Desired{}, fmt.Errorf("parse desired state: %w", err)
	}
	return d, d.validate()
}

// DefaultDesired returns the embedded default.
func DefaultDesired() Desired {
	var d Desired
	if err := toml.Unmarshal(defaultDesired, &d); err != nil {
		panic(err)
	}
	return d
}

// Bot returns the bot named name.
func (d Desired) Bot(name string) (Bot, bool) {
	for _, b := range d.Bots {
		if b.Name == name {
			return b, true
		}
	}
	return Bot{}, false
}

func (d Desired) validate() error {
	if d.Org == "" {
		return errors.New("desired state: org is required")
	}
	teams := map[string]bool{}
	for _, t := range d.Teams {
		teams[t.Name] = true
	}
	for _, b := range d.Bots {
		if b.Team != "" && !teams[b.Team] {
			return fmt.Errorf("desired state: bot %s: unknown team %q", b.Name, b.Team)
		}
		if len(b.Scopes) == 0 {
			return fmt.Errorf("desired state: bot %s: token_scopes is required", b.Name)
		}
	}
	return nil
}
