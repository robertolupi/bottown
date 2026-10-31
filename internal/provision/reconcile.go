// Package provision converges a Forgejo instance to bottown's desired state:
// the bootstrap admin, the owner and bot accounts, their tokens, the org,
// teams, labels and per-repository branch protection. Every step is
// get-then-create-or-patch, so a second run makes no writes.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/secrets"
)

// AdminLogin is the site admin bottown itself uses. No human logs in as it.
const AdminLogin = "bottown-admin"

// Owner identifies the human product owner.
type Owner struct {
	Login    string
	Email    string
	FullName string
}

// Admin is the subset of the `forgejo admin` CLI the reconciler needs.
type Admin interface {
	CreateAdmin(ctx context.Context, username, email, password string) error
	GenerateToken(ctx context.Context, username, name string, scopes []string) (string, error)
}

// Report lists the writes a reconcile pass made.
type Report struct {
	Changes []string `json:"changes"`
}

func (r *Report) add(format string, args ...any) {
	r.Changes = append(r.Changes, fmt.Sprintf(format, args...))
}

// Reconciler converges Forgejo to Desired.
type Reconciler struct {
	BaseURL string // Forgejo API base, e.g. http://127.0.0.1:3000
	Admin   Admin
	Store   *secrets.Store
	Desired config.Desired
	Owner   Owner
	Log     *slog.Logger

	api *forgejo.Client
}

// WaitReady blocks until Forgejo's unauthenticated /api/healthz passes.
// (/api/v1/version is not usable: REQUIRE_SIGNIN_VIEW makes it 403 for
// anonymous callers.)
func (r *Reconciler) WaitReady(ctx context.Context) error {
	hc := &http.Client{Timeout: 3 * time.Second}
	var last error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.BaseURL+"/api/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := hc.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("healthz: %s", resp.Status)
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("forgejo not ready: %w (last error: %v)", ctx.Err(), last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Bootstrap ensures bottown holds a working site-admin token. The first user
// creation can fail until the database accepts writes, so it retries.
func (r *Reconciler) Bootstrap(ctx context.Context, rep *Report) error {
	st, err := r.Store.Read()
	if err != nil {
		return err
	}
	if st.AdminToken != "" {
		if _, err := forgejo.New(r.BaseURL, st.AdminToken).Self(ctx); err == nil {
			r.api = forgejo.New(r.BaseURL, st.AdminToken)
			return nil
		}
	}
	var token string
	err = retry(ctx, 60*time.Second, func() error {
		err := r.Admin.CreateAdmin(ctx, AdminLogin, AdminLogin+"@bottown.local", secrets.RandomString(24))
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
		token, err = r.Admin.GenerateToken(ctx, AdminLogin, tokenName(), []string{"all"})
		return err
	})
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if err := r.Store.Update(func(s *secrets.State) error { s.AdminToken = token; return nil }); err != nil {
		return err
	}
	r.api = forgejo.New(r.BaseURL, token)
	rep.add("minted admin token for %s", AdminLogin)
	return nil
}

// API returns the admin client; Bootstrap must have succeeded.
func (r *Reconciler) API() *forgejo.Client { return r.api }

// Run bootstraps and reconciles everything, including protection on every
// non-empty org repository.
func (r *Reconciler) Run(ctx context.Context) (Report, error) {
	var rep Report
	if err := r.Bootstrap(ctx, &rep); err != nil {
		return rep, err
	}
	steps := []func(context.Context, *Report) error{
		r.ensureOwner,
		r.ensureBots,
		r.ensureOrg,
		r.ensureTeams,
		r.ensureLabels,
		r.ensureTokens,
		r.ensureAllRepos,
	}
	for _, step := range steps {
		if err := step(ctx, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}

func (r *Reconciler) ensureOwner(ctx context.Context, rep *Report) error {
	o := r.Owner
	if o.Login == "" {
		return errors.New("owner login is not set")
	}
	if o.Email == "" {
		o.Email = o.Login + "@bottown.local"
	}
	u, err := r.api.GetUser(ctx, o.Login)
	if forgejo.IsNotFound(err) {
		pw := secrets.RandomString(18)
		no := false
		if _, err := r.api.AdminCreateUser(ctx, forgejo.CreateUserOption{
			Username: o.Login, Email: o.Email, FullName: o.FullName,
			Password: pw, MustChangePassword: &no,
		}); err != nil {
			return err
		}
		if err := r.Store.Update(func(s *secrets.State) error { s.OwnerPassword = pw; return nil }); err != nil {
			return err
		}
		rep.add("created owner %s", o.Login)
		u, err = r.api.GetUser(ctx, o.Login)
	}
	if err != nil {
		return err
	}
	if !u.IsAdmin {
		yes := true
		if err := r.api.AdminEditUser(ctx, o.Login, forgejo.EditUserOption{LoginName: o.Login, Admin: &yes}); err != nil {
			return err
		}
		rep.add("made owner %s a site admin", o.Login)
	}
	return nil
}

func (r *Reconciler) ensureBots(ctx context.Context, rep *Report) error {
	for _, b := range r.Desired.Bots {
		u, err := r.api.GetUser(ctx, b.Name)
		if forgejo.IsNotFound(err) {
			no := false
			// The password is discarded: bots are token-only.
			if _, err := r.api.AdminCreateUser(ctx, forgejo.CreateUserOption{
				Username: b.Name, Email: b.Name + "@bottown.local", FullName: b.FullName,
				Password: secrets.RandomString(24), MustChangePassword: &no,
			}); err != nil {
				return err
			}
			rep.add("created bot %s", b.Name)
			u, err = r.api.GetUser(ctx, b.Name)
		}
		if err != nil {
			return err
		}
		// prohibit_login would also reject the bot's API tokens, so it must
		// stay off; the discarded random password is what keeps bots out of
		// the web UI.
		if u.ProhibitLogin || u.FullName != b.FullName {
			full, no := b.FullName, false
			if err := r.api.AdminEditUser(ctx, b.Name, forgejo.EditUserOption{
				LoginName: b.Name, FullName: &full, ProhibitLogin: &no,
			}); err != nil {
				return err
			}
			rep.add("updated bot %s", b.Name)
		}
	}
	return nil
}

func (r *Reconciler) ensureOrg(ctx context.Context, rep *Report) error {
	_, err := r.api.GetOrg(ctx, r.Desired.Org)
	if forgejo.IsNotFound(err) {
		if _, err := r.api.CreateOrg(ctx, r.Desired.Org); err != nil {
			return err
		}
		rep.add("created org %s", r.Desired.Org)
		return nil
	}
	return err
}

// allUnits are the repository units a team's units map is completed with;
// units not named in the desired team get no access.
var allUnits = []string{
	"repo.code", "repo.issues", "repo.pulls", "repo.releases", "repo.wiki",
	"repo.ext_wiki", "repo.ext_issues", "repo.projects", "repo.packages", "repo.actions",
}

func teamUnits(t config.Team) map[string]string {
	m := map[string]string{}
	for _, u := range allUnits {
		m[u] = "none"
	}
	for u, p := range t.Units {
		m[u] = p
	}
	return m
}

func teamPermission(units map[string]string) string {
	for _, p := range units {
		if p == "write" {
			return "write"
		}
	}
	return "read"
}

func (r *Reconciler) ensureTeams(ctx context.Context, rep *Report) error {
	org := r.Desired.Org
	teams, err := r.api.ListOrgTeams(ctx, org)
	if err != nil {
		return err
	}
	byName := map[string]forgejo.Team{}
	for _, t := range teams {
		byName[t.Name] = t
	}
	// The owner joins the built-in Owners team, found by name.
	owners, ok := byName["Owners"]
	if !ok {
		return fmt.Errorf("org %s has no Owners team", org)
	}
	if err := r.ensureMember(ctx, rep, owners, r.Owner.Login); err != nil {
		return err
	}
	for _, dt := range r.Desired.Teams {
		units := teamUnits(dt)
		opt := forgejo.TeamOption{
			Name:                    dt.Name,
			Description:             "managed by bottown",
			Permission:              teamPermission(units),
			IncludesAllRepositories: true,
			Units:                   slices.Sorted(maps.Keys(units)),
			UnitsMap:                units,
		}
		t, ok := byName[dt.Name]
		if !ok {
			t, err = r.api.CreateTeam(ctx, org, opt)
			if err != nil {
				return err
			}
			rep.add("created team %s", dt.Name)
		} else if !t.IncludesAllRepositories || !unitsEqual(t.UnitsMap, units) {
			if err := r.api.EditTeam(ctx, t.ID, opt); err != nil {
				return err
			}
			rep.add("updated team %s", dt.Name)
		}
		for _, b := range r.Desired.Bots {
			if b.Team == dt.Name {
				if err := r.ensureMember(ctx, rep, t, b.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func unitsEqual(have, want map[string]string) bool {
	for u, p := range want {
		h, ok := have[u]
		if !ok && p == "none" {
			continue
		}
		if h != p {
			return false
		}
	}
	return true
}

func (r *Reconciler) ensureMember(ctx context.Context, rep *Report, t forgejo.Team, login string) error {
	members, err := r.api.ListTeamMembers(ctx, t.ID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.Login == login {
			return nil
		}
	}
	if err := r.api.AddTeamMember(ctx, t.ID, login); err != nil {
		return err
	}
	rep.add("added %s to team %s", login, t.Name)
	return nil
}

func (r *Reconciler) ensureLabels(ctx context.Context, rep *Report) error {
	org := r.Desired.Org
	have, err := r.api.ListOrgLabels(ctx, org)
	if err != nil {
		return err
	}
	byName := map[string]forgejo.Label{}
	for _, l := range have {
		byName[l.Name] = l
	}
	for _, dl := range r.Desired.Labels {
		want := forgejo.Label{Name: dl.Name, Color: dl.Color, Description: dl.Description, Exclusive: dl.Exclusive}
		l, ok := byName[dl.Name]
		switch {
		case !ok:
			if err := r.api.CreateOrgLabel(ctx, org, want); err != nil {
				return err
			}
			rep.add("created label %s", dl.Name)
		case strings.TrimPrefix(l.Color, "#") != strings.TrimPrefix(want.Color, "#") ||
			l.Description != want.Description || l.Exclusive != want.Exclusive:
			if err := r.api.EditOrgLabel(ctx, org, l.ID, want); err != nil {
				return err
			}
			rep.add("updated label %s", dl.Name)
		}
	}
	return nil
}

// ensureTokens mints a Forgejo token for every actor whose stored token is
// missing, rejected, or minted with different scopes.
func (r *Reconciler) ensureTokens(ctx context.Context, rep *Report) error {
	actors := []struct {
		login  string
		scopes []string
	}{{r.Owner.Login, r.Desired.OwnerScope}}
	for _, b := range r.Desired.Bots {
		actors = append(actors, struct {
			login  string
			scopes []string
		}{b.Name, b.Scopes})
	}
	st, err := r.Store.Read()
	if err != nil {
		return err
	}
	for _, a := range actors {
		scopes := a.scopes
		if len(scopes) == 0 {
			scopes = []string{"all"}
		}
		if t, ok := st.Tokens[a.login]; ok && slices.Equal(t.Scopes, scopes) {
			u, err := forgejo.New(r.BaseURL, t.Token).Self(ctx)
			if err == nil && u.Login == a.login {
				continue
			}
			if err != nil && !forgejo.IsStatus(err, http.StatusUnauthorized) && !forgejo.IsStatus(err, http.StatusForbidden) {
				return err
			}
		}
		name := tokenName()
		tok, err := r.Admin.GenerateToken(ctx, a.login, name, scopes)
		if err != nil {
			return err
		}
		if err := r.Store.Update(func(s *secrets.State) error {
			if s.Tokens == nil {
				s.Tokens = map[string]secrets.ActorToken{}
			}
			if old, ok := s.Tokens[a.login]; ok {
				// Forgejo only deletes tokens with the user's own basic auth,
				// so a superseded token stays valid until removed by hand.
				r.log().Warn("superseded token left on forge", "actor", a.login, "token_name", old.Name)
			}
			s.Tokens[a.login] = secrets.ActorToken{Name: name, Scopes: scopes, Token: tok}
			return nil
		}); err != nil {
			return err
		}
		rep.add("minted token for %s (%s)", a.login, strings.Join(scopes, ","))
	}
	return nil
}

func (r *Reconciler) ensureAllRepos(ctx context.Context, rep *Report) error {
	repos, err := r.api.ListOrgRepos(ctx, r.Desired.Org)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if repo.Empty || repo.DefaultBranch == "" {
			continue
		}
		if err := r.ensureProtection(ctx, rep, repo.Name, repo.DefaultBranch); err != nil {
			return err
		}
	}
	return nil
}

// EnsureRepo creates org/name if needed. Once the repository has content it
// sets the default branch and applies protection to it.
func (r *Reconciler) EnsureRepo(ctx context.Context, name, branch string) (forgejo.Repo, Report, error) {
	var rep Report
	org := r.Desired.Org
	repo, err := r.api.GetRepo(ctx, org, name)
	if forgejo.IsNotFound(err) {
		repo, err = r.api.CreateOrgRepo(ctx, org, name, branch)
		if err != nil {
			return repo, rep, err
		}
		rep.add("created repo %s/%s", org, name)
		return repo, rep, nil
	}
	if err != nil || repo.Empty {
		return repo, rep, err
	}
	if branch != "" && repo.DefaultBranch != branch {
		if err := r.api.SetDefaultBranch(ctx, org, name, branch); err != nil {
			return repo, rep, err
		}
		rep.add("set default branch of %s to %s", name, branch)
		repo.DefaultBranch = branch
	}
	err = r.ensureProtection(ctx, &rep, name, repo.DefaultBranch)
	return repo, rep, err
}

// WantProtection is the protection rule for branch derived from Desired.
func (r *Reconciler) WantProtection(branch string) forgejo.BranchProtection {
	p := r.Desired.Protection
	owner := r.Owner.Login
	return forgejo.BranchProtection{
		RuleName:                 branch,
		EnablePush:               true,
		EnablePushWhitelist:      true,
		PushWhitelistUsernames:   []string{owner},
		EnableMergeWhitelist:     true,
		MergeWhitelistUsernames:  withOwner(p.MergeWhitelist, owner),
		EnableApprovalsWhitelist: true,
		ApprovalsWhitelistUsers:  withOwner(p.ApprovalsWhitelist, owner),
		RequiredApprovals:        p.RequiredApprovals,
		DismissStaleApprovals:    p.DismissStaleApprovals,
		BlockOnRejectedReviews:   p.BlockOnRejectedReviews,
		ApplyToAdmins:            p.ApplyToAdmins,
	}
}

func (r *Reconciler) ensureProtection(ctx context.Context, rep *Report, repo, branch string) error {
	org := r.Desired.Org
	want := r.WantProtection(branch)
	have, err := r.api.GetBranchProtection(ctx, org, repo, branch)
	if forgejo.IsNotFound(err) {
		want.BranchName = branch
		if err := r.api.CreateBranchProtection(ctx, org, repo, want); err != nil {
			return err
		}
		rep.add("protected %s/%s:%s", org, repo, branch)
		return nil
	}
	if err != nil {
		return err
	}
	if diff := ProtectionDiff(have, want); len(diff) > 0 {
		if err := r.api.EditBranchProtection(ctx, org, repo, branch, want); err != nil {
			return err
		}
		rep.add("updated protection %s/%s:%s (%s)", org, repo, branch, strings.Join(diff, ", "))
	}
	return nil
}

// ProtectionDiff names the managed fields where have differs from want.
func ProtectionDiff(have, want forgejo.BranchProtection) []string {
	var d []string
	chk := func(name string, eq bool) {
		if !eq {
			d = append(d, name)
		}
	}
	chk("enable_push", have.EnablePush == want.EnablePush)
	chk("enable_push_whitelist", have.EnablePushWhitelist == want.EnablePushWhitelist)
	chk("push_whitelist_usernames", sameSet(have.PushWhitelistUsernames, want.PushWhitelistUsernames))
	chk("enable_force_push", have.EnableForcePush == want.EnableForcePush)
	chk("enable_merge_whitelist", have.EnableMergeWhitelist == want.EnableMergeWhitelist)
	chk("merge_whitelist_usernames", sameSet(have.MergeWhitelistUsernames, want.MergeWhitelistUsernames))
	chk("enable_approvals_whitelist", have.EnableApprovalsWhitelist == want.EnableApprovalsWhitelist)
	chk("approvals_whitelist_username", sameSet(have.ApprovalsWhitelistUsers, want.ApprovalsWhitelistUsers))
	chk("required_approvals", have.RequiredApprovals == want.RequiredApprovals)
	chk("dismiss_stale_approvals", have.DismissStaleApprovals == want.DismissStaleApprovals)
	chk("block_on_rejected_reviews", have.BlockOnRejectedReviews == want.BlockOnRejectedReviews)
	chk("apply_to_admins", have.ApplyToAdmins == want.ApplyToAdmins)
	return d
}

func withOwner(list []string, owner string) []string {
	out := slices.Clone(list)
	if !slices.Contains(out, owner) {
		out = append(out, owner)
	}
	return out
}

func sameSet(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func tokenName() string { return fmt.Sprintf("bottown-%d", time.Now().UnixNano()) }

func retry(ctx context.Context, d time.Duration, fn func() error) error {
	deadline := time.Now().Add(d)
	for {
		err := fn()
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
