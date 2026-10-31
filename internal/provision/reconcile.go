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
	"net/http"
	"slices"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

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

// Admin is the subset of forgejo.AdminClient the reconciler needs.
type Admin interface {
	CreateAdmin(ctx context.Context, username, email, password string) error
	GenerateToken(ctx context.Context, username, name string, scopes []string) (string, error)
	ChangePassword(ctx context.Context, username, password string) error
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

	adminToken string
}

// WaitReady blocks until Forgejo's /api/healthz passes.
func (r *Reconciler) WaitReady(ctx context.Context) error {
	var last error
	for {
		if last = forgejo.Healthy(ctx, r.BaseURL); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("forgejo not ready: %w (last error: %v)", ctx.Err(), last)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// API returns an admin client bound to ctx; Bootstrap must have succeeded.
func (r *Reconciler) API(ctx context.Context) *forgejo.Client {
	return forgejo.New(ctx, r.BaseURL, r.adminToken)
}

// Bootstrap ensures bottown holds a working site-admin token. The first user
// creation can fail until the database accepts writes, so it retries.
func (r *Reconciler) Bootstrap(ctx context.Context, rep *Report) error {
	st, err := r.Store.Read()
	if err != nil {
		return err
	}
	if st.AdminToken != "" {
		if _, _, err := forgejo.New(ctx, r.BaseURL, st.AdminToken).GetMyUserInfo(); err == nil {
			r.adminToken = st.AdminToken
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
	r.adminToken = token
	rep.add("minted admin token for %s", AdminLogin)
	return nil
}

// Run bootstraps and reconciles everything, including protection on every
// non-empty org repository.
func (r *Reconciler) Run(ctx context.Context) (Report, error) {
	var rep Report
	if err := r.Bootstrap(ctx, &rep); err != nil {
		return rep, err
	}
	api := r.API(ctx)
	steps := []func(*forgejo.Client, *Report) error{
		r.ensureOwner,
		r.ensureBots,
		r.ensureOrg,
		r.ensureTeams,
		r.ensureLabels,
		func(api *forgejo.Client, rep *Report) error { return r.ensureTokens(ctx, rep) },
		r.ensureAllRepos,
	}
	for _, step := range steps {
		if err := step(api, &rep); err != nil {
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

func (r *Reconciler) ensureOwner(api *forgejo.Client, rep *Report) error {
	o := r.Owner
	if o.Login == "" {
		return errors.New("owner login is not set")
	}
	if o.Email == "" {
		o.Email = o.Login + "@bottown.local"
	}
	u, resp, err := api.GetUserInfo(o.Login)
	if forgejo.IsNotFound(resp, err) {
		pw := secrets.RandomString(18)
		no := false
		if _, _, err := api.AdminCreateUser(sdk.CreateUserOption{
			Username: o.Login, Email: o.Email, FullName: o.FullName,
			Password: pw, MustChangePassword: &no,
		}); err != nil {
			return err
		}
		if err := r.Store.Update(func(s *secrets.State) error { s.OwnerPassword = pw; return nil }); err != nil {
			return err
		}
		rep.add("created owner %s", o.Login)
		u, _, err = api.GetUserInfo(o.Login)
	}
	if err != nil {
		return err
	}
	if !u.IsAdmin {
		yes := true
		if _, err := api.AdminEditUser(o.Login, sdk.EditUserOption{LoginName: o.Login, Admin: &yes}); err != nil {
			return err
		}
		rep.add("made owner %s a site admin", o.Login)
	}
	return nil
}

func (r *Reconciler) ensureBots(api *forgejo.Client, rep *Report) error {
	for _, b := range r.Desired.Bots {
		u, resp, err := api.GetUserInfo(b.Name)
		if forgejo.IsNotFound(resp, err) {
			no := false
			// The password is discarded: bots are token-only.
			if _, _, err := api.AdminCreateUser(sdk.CreateUserOption{
				Username: b.Name, Email: b.Name + "@bottown.local", FullName: b.FullName,
				Password: secrets.RandomString(24), MustChangePassword: &no,
			}); err != nil {
				return err
			}
			rep.add("created bot %s", b.Name)
			u, _, err = api.GetUserInfo(b.Name)
		}
		if err != nil {
			return err
		}
		// prohibit_login would also reject the bot's API tokens, so it must
		// stay off; the discarded random password is what keeps bots out of
		// the web UI.
		if u.ProhibitLogin || u.FullName != b.FullName {
			full, no := b.FullName, false
			if _, err := api.AdminEditUser(b.Name, sdk.EditUserOption{
				LoginName: b.Name, FullName: &full, ProhibitLogin: &no,
			}); err != nil {
				return err
			}
			rep.add("updated bot %s", b.Name)
		}
	}
	return nil
}

func (r *Reconciler) ensureOrg(api *forgejo.Client, rep *Report) error {
	_, resp, err := api.GetOrg(r.Desired.Org)
	if forgejo.IsNotFound(resp, err) {
		if _, _, err := api.CreateOrg(sdk.CreateOrgOption{Name: r.Desired.Org, Visibility: sdk.VisibleTypePrivate}); err != nil {
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

func teamPermission(units map[string]string) sdk.AccessMode {
	for _, p := range units {
		if p == "write" {
			return sdk.AccessModeWrite
		}
	}
	return sdk.AccessModeRead
}

func (r *Reconciler) ensureTeams(api *forgejo.Client, rep *Report) error {
	org := r.Desired.Org
	teams, _, err := api.ListOrgTeams(org, sdk.ListTeamsOptions{ListOptions: sdk.ListOptions{PageSize: 50}})
	if err != nil {
		return err
	}
	byName := map[string]*sdk.Team{}
	for _, t := range teams {
		byName[t.Name] = t
	}
	// The owner joins the built-in Owners team, found by name.
	owners, ok := byName["Owners"]
	if !ok {
		return fmt.Errorf("org %s has no Owners team", org)
	}
	if err := r.ensureMember(api, rep, owners, r.Owner.Login); err != nil {
		return err
	}
	for _, dt := range r.Desired.Teams {
		units := teamUnits(dt)
		desc, all, perm := "managed by bottown", true, teamPermission(units)
		t, ok := byName[dt.Name]
		if !ok {
			t, _, err = api.CreateTeam(org, sdk.CreateTeamOption{
				Name: dt.Name, Description: desc, Permission: perm,
				IncludesAllRepositories: all, UnitsMap: units,
			})
			if err != nil {
				return err
			}
			rep.add("created team %s", dt.Name)
		} else if !t.IncludesAllRepositories || !unitsEqual(t.UnitsMap, units) {
			if _, err := api.EditTeam(t.ID, sdk.EditTeamOption{
				Name: dt.Name, Description: &desc, Permission: perm,
				IncludesAllRepositories: &all, UnitsMap: units,
			}); err != nil {
				return err
			}
			rep.add("updated team %s", dt.Name)
		}
		for _, b := range r.Desired.Bots {
			if b.Team == dt.Name {
				if err := r.ensureMember(api, rep, t, b.Name); err != nil {
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

func (r *Reconciler) ensureMember(api *forgejo.Client, rep *Report, t *sdk.Team, login string) error {
	members, _, err := api.ListTeamMembers(t.ID, sdk.ListTeamMembersOptions{ListOptions: sdk.ListOptions{PageSize: 50}})
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.UserName == login {
			return nil
		}
	}
	if _, err := api.AddTeamMember(t.ID, login); err != nil {
		return err
	}
	rep.add("added %s to team %s", login, t.Name)
	return nil
}

func (r *Reconciler) ensureLabels(api *forgejo.Client, rep *Report) error {
	org := r.Desired.Org
	have, err := api.ListOrgLabels(org)
	if err != nil {
		return err
	}
	byName := map[string]forgejo.OrgLabel{}
	for _, l := range have {
		byName[l.Name] = l
	}
	for _, dl := range r.Desired.Labels {
		want := forgejo.OrgLabel{Name: dl.Name, Color: dl.Color, Description: dl.Description, Exclusive: dl.Exclusive}
		l, ok := byName[dl.Name]
		switch {
		case !ok:
			if err := api.CreateOrgLabel(org, want); err != nil {
				return err
			}
			rep.add("created label %s", dl.Name)
		case strings.TrimPrefix(l.Color, "#") != strings.TrimPrefix(want.Color, "#") ||
			l.Description != want.Description || l.Exclusive != want.Exclusive:
			if err := api.EditOrgLabel(org, l.ID, want); err != nil {
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
	type actor struct {
		login  string
		scopes []string
	}
	actors := []actor{{r.Owner.Login, r.Desired.OwnerScope}}
	for _, b := range r.Desired.Bots {
		actors = append(actors, actor{b.Name, b.Scopes})
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
			u, resp, err := forgejo.New(ctx, r.BaseURL, t.Token).GetMyUserInfo()
			if err == nil && u.UserName == a.login {
				continue
			}
			if err != nil && !forgejo.StatusIs(resp, err, http.StatusUnauthorized) && !forgejo.StatusIs(resp, err, http.StatusForbidden) {
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

func (r *Reconciler) ensureAllRepos(api *forgejo.Client, rep *Report) error {
	for page := 1; ; page++ {
		repos, _, err := api.ListOrgRepos(r.Desired.Org, sdk.ListOrgReposOptions{ListOptions: sdk.ListOptions{Page: page, PageSize: 50}})
		if err != nil {
			return err
		}
		for _, repo := range repos {
			if repo.Empty || repo.DefaultBranch == "" {
				continue
			}
			if err := r.ensureProtection(api, rep, repo.Name, repo.DefaultBranch); err != nil {
				return err
			}
		}
		if len(repos) < 50 {
			return nil
		}
	}
}

// Repo is what EnsureRepo reports about a repository.
type Repo struct {
	FullName      string
	Empty         bool
	DefaultBranch string
}

// EnsureRepo creates org/name if needed. Once the repository has content it
// sets the default branch and applies protection to it.
func (r *Reconciler) EnsureRepo(ctx context.Context, name, branch string) (Repo, Report, error) {
	var rep Report
	api := r.API(ctx)
	org := r.Desired.Org
	repo, resp, err := api.GetRepo(org, name)
	if forgejo.IsNotFound(resp, err) {
		repo, _, err = api.CreateOrgRepo(org, sdk.CreateRepoOption{Name: name, Private: true, DefaultBranch: branch})
		if err != nil {
			return Repo{}, rep, err
		}
		rep.add("created repo %s/%s", org, name)
		return Repo{FullName: repo.FullName, Empty: true, DefaultBranch: repo.DefaultBranch}, rep, nil
	}
	if err != nil {
		return Repo{}, rep, err
	}
	out := Repo{FullName: repo.FullName, Empty: repo.Empty, DefaultBranch: repo.DefaultBranch}
	if repo.Empty {
		return out, rep, nil
	}
	if branch != "" && repo.DefaultBranch != branch {
		if _, _, err := api.EditRepo(org, name, sdk.EditRepoOption{DefaultBranch: &branch}); err != nil {
			return out, rep, err
		}
		rep.add("set default branch of %s to %s", name, branch)
		out.DefaultBranch = branch
	}
	err = r.ensureProtection(api, &rep, name, out.DefaultBranch)
	return out, rep, err
}

// Protection is the branch protection bottown manages, as one flat value
// (the SDK's BranchProtection plus the apply_to_admins extension).
type Protection struct {
	EnablePush                  bool
	EnablePushWhitelist         bool
	PushWhitelistUsernames      []string
	EnableMergeWhitelist        bool
	MergeWhitelistUsernames     []string
	EnableApprovalsWhitelist    bool
	ApprovalsWhitelistUsernames []string
	RequiredApprovals           int64
	DismissStaleApprovals       bool
	BlockOnRejectedReviews      bool
	ApplyToAdmins               bool
}

// WantProtection is the protection rule derived from Desired. The owner is
// always on the push, merge and approval allow-lists.
func (r *Reconciler) WantProtection() Protection {
	p := r.Desired.Protection
	owner := r.Owner.Login
	return Protection{
		EnablePush:                  true,
		EnablePushWhitelist:         true,
		PushWhitelistUsernames:      []string{owner},
		EnableMergeWhitelist:        true,
		MergeWhitelistUsernames:     withOwner(p.MergeWhitelist, owner),
		EnableApprovalsWhitelist:    true,
		ApprovalsWhitelistUsernames: withOwner(p.ApprovalsWhitelist, owner),
		RequiredApprovals:           p.RequiredApprovals,
		DismissStaleApprovals:       p.DismissStaleApprovals,
		BlockOnRejectedReviews:      p.BlockOnRejectedReviews,
		ApplyToAdmins:               p.ApplyToAdmins,
	}
}

func (r *Reconciler) ensureProtection(api *forgejo.Client, rep *Report, repo, branch string) error {
	org := r.Desired.Org
	want := r.WantProtection()
	bp, resp, err := api.GetBranchProtection(org, repo, branch)
	if forgejo.IsNotFound(resp, err) {
		if _, _, err := api.CreateBranchProtection(org, repo, sdk.CreateBranchProtectionOption{
			BranchName:                  branch,
			RuleName:                    branch,
			EnablePush:                  want.EnablePush,
			EnablePushWhitelist:         want.EnablePushWhitelist,
			PushWhitelistUsernames:      want.PushWhitelistUsernames,
			EnableMergeWhitelist:        want.EnableMergeWhitelist,
			MergeWhitelistUsernames:     want.MergeWhitelistUsernames,
			EnableApprovalsWhitelist:    want.EnableApprovalsWhitelist,
			ApprovalsWhitelistUsernames: want.ApprovalsWhitelistUsernames,
			RequiredApprovals:           want.RequiredApprovals,
			DismissStaleApprovals:       want.DismissStaleApprovals,
			BlockOnRejectedReviews:      want.BlockOnRejectedReviews,
		}); err != nil {
			return err
		}
		if err := api.SetApplyToAdmins(org, repo, branch, want.ApplyToAdmins); err != nil {
			return err
		}
		rep.add("protected %s/%s:%s", org, repo, branch)
		return nil
	}
	if err != nil {
		return err
	}
	ata, err := api.ApplyToAdmins(org, repo, branch)
	if err != nil {
		return err
	}
	have := Protection{
		EnablePush:                  bp.EnablePush,
		EnablePushWhitelist:         bp.EnablePushWhitelist,
		PushWhitelistUsernames:      bp.PushWhitelistUsernames,
		EnableMergeWhitelist:        bp.EnableMergeWhitelist,
		MergeWhitelistUsernames:     bp.MergeWhitelistUsernames,
		EnableApprovalsWhitelist:    bp.EnableApprovalsWhitelist,
		ApprovalsWhitelistUsernames: bp.ApprovalsWhitelistUsernames,
		RequiredApprovals:           bp.RequiredApprovals,
		DismissStaleApprovals:       bp.DismissStaleApprovals,
		BlockOnRejectedReviews:      bp.BlockOnRejectedReviews,
		ApplyToAdmins:               ata,
	}
	diff := ProtectionDiff(have, want)
	if len(diff) == 0 {
		return nil
	}
	if _, _, err := api.EditBranchProtection(org, repo, branch, sdk.EditBranchProtectionOption{
		EnablePush:                  &want.EnablePush,
		EnablePushWhitelist:         &want.EnablePushWhitelist,
		PushWhitelistUsernames:      want.PushWhitelistUsernames,
		EnableMergeWhitelist:        &want.EnableMergeWhitelist,
		MergeWhitelistUsernames:     want.MergeWhitelistUsernames,
		EnableApprovalsWhitelist:    &want.EnableApprovalsWhitelist,
		ApprovalsWhitelistUsernames: want.ApprovalsWhitelistUsernames,
		RequiredApprovals:           &want.RequiredApprovals,
		DismissStaleApprovals:       &want.DismissStaleApprovals,
		BlockOnRejectedReviews:      &want.BlockOnRejectedReviews,
	}); err != nil {
		return err
	}
	if have.ApplyToAdmins != want.ApplyToAdmins {
		if err := api.SetApplyToAdmins(org, repo, branch, want.ApplyToAdmins); err != nil {
			return err
		}
	}
	rep.add("updated protection %s/%s:%s (%s)", org, repo, branch, strings.Join(diff, ", "))
	return nil
}

// ProtectionDiff names the fields where have differs from want.
func ProtectionDiff(have, want Protection) []string {
	var d []string
	chk := func(name string, eq bool) {
		if !eq {
			d = append(d, name)
		}
	}
	chk("enable_push", have.EnablePush == want.EnablePush)
	chk("enable_push_whitelist", have.EnablePushWhitelist == want.EnablePushWhitelist)
	chk("push_whitelist_usernames", sameSet(have.PushWhitelistUsernames, want.PushWhitelistUsernames))
	chk("enable_merge_whitelist", have.EnableMergeWhitelist == want.EnableMergeWhitelist)
	chk("merge_whitelist_usernames", sameSet(have.MergeWhitelistUsernames, want.MergeWhitelistUsernames))
	chk("enable_approvals_whitelist", have.EnableApprovalsWhitelist == want.EnableApprovalsWhitelist)
	chk("approvals_whitelist_username", sameSet(have.ApprovalsWhitelistUsernames, want.ApprovalsWhitelistUsernames))
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
