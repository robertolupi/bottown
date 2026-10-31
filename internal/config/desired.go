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
	Org         string     `toml:"org"`
	Teams       []Team     `toml:"teams"`
	Actors      []Actor    `toml:"actors"`
	ActorTeam   string     `toml:"actor_team"`
	ActorScopes []string   `toml:"actor_token_scopes"`
	Manager     Account    `toml:"manager"`
	Retired     []string   `toml:"retired"`
	Labels      []Label    `toml:"labels"`
	Protection  Protection `toml:"protection"`
	OwnerScope  []string   `toml:"owner_token_scopes"`
	// DisableActorSelfStart stops actors from starting runs for themselves
	// (from inside a harness); only the owner can then start runs.
	DisableActorSelfStart bool `toml:"disable_actor_self_start"`
}

// Team is an organization team covering all repositories.
type Team struct {
	Name  string            `toml:"name"`
	Units map[string]string `toml:"units"` // unit -> read|write
}

// Actor is the forge identity of a coding agent. Roles (implementer,
// verifier) are not part of it: a run grants one.
type Actor struct {
	Name     string `toml:"name"`
	Family   string `toml:"family"`  // model vendor, for the cross-family review rule
	Harness  string `toml:"harness"` // which CLI runs it
	FullName string `toml:"full_name"`
}

// Account is a token-only forge account bottown reconciles.
type Account struct {
	Name     string   `toml:"name"`
	FullName string   `toml:"full_name"`
	Team     string   `toml:"team"`
	Scopes   []string `toml:"token_scopes"`
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

// Actor returns the actor named name.
func (d Desired) Actor(name string) (Actor, bool) {
	for _, a := range d.Actors {
		if a.Name == name {
			return a, true
		}
	}
	return Actor{}, false
}

// Accounts returns every bot account to reconcile: the actors, then the
// manager.
func (d Desired) Accounts() []Account {
	var out []Account
	for _, a := range d.Actors {
		full := a.FullName
		if full == "" {
			full = a.Name + " (" + a.Family + ")"
		}
		out = append(out, Account{Name: a.Name, FullName: full, Team: d.ActorTeam, Scopes: d.ActorScopes})
	}
	if d.Manager.Name != "" {
		out = append(out, d.Manager)
	}
	return out
}

// ActorNames lists the actors' logins.
func (d Desired) ActorNames() []string {
	var out []string
	for _, a := range d.Actors {
		out = append(out, a.Name)
	}
	return out
}

func (d Desired) validate() error {
	if d.Org == "" {
		return errors.New("desired state: org is required")
	}
	teams := map[string]bool{}
	for _, t := range d.Teams {
		teams[t.Name] = true
	}
	seen := map[string]bool{}
	for _, a := range d.Accounts() {
		switch {
		case seen[a.Name]:
			return fmt.Errorf("desired state: account %s defined twice", a.Name)
		case a.Team != "" && !teams[a.Team]:
			return fmt.Errorf("desired state: account %s: unknown team %q", a.Name, a.Team)
		case len(a.Scopes) == 0:
			return fmt.Errorf("desired state: account %s: token scopes are required", a.Name)
		}
		seen[a.Name] = true
	}
	for _, a := range d.Actors {
		if a.Family == "" {
			return fmt.Errorf("desired state: actor %s: family is required", a.Name)
		}
	}
	for _, r := range d.Retired {
		if seen[r] {
			return fmt.Errorf("desired state: %s is both active and retired", r)
		}
	}
	return nil
}
