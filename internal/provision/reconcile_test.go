package provision

import (
	"testing"

	"github.com/robertolupi/bottown/internal/config"
)

func TestWantProtectionHasNoDiffWithItself(t *testing.T) {
	r := &Reconciler{Desired: config.DefaultDesired(), Owner: Owner{Login: "owner"}}
	want := r.WantProtection()
	have := want
	// Forgejo returns allow-lists in its own order.
	have.MergeWhitelistUsernames = []string{"owner", "manager"}
	if d := ProtectionDiff(have, want); len(d) != 0 {
		t.Fatalf("diff = %v", d)
	}
	have.ApplyToAdmins = false
	if d := ProtectionDiff(have, want); len(d) != 1 || d[0] != "apply_to_admins" {
		t.Fatalf("diff = %v", d)
	}
}

func TestOwnerAlwaysAllowListed(t *testing.T) {
	r := &Reconciler{Desired: config.DefaultDesired(), Owner: Owner{Login: "owner"}}
	p := r.WantProtection()
	for name, list := range map[string][]string{
		"push": p.PushWhitelistUsernames, "merge": p.MergeWhitelistUsernames, "approvals": p.ApprovalsWhitelistUsernames,
	} {
		found := false
		for _, u := range list {
			found = found || u == "owner"
		}
		if !found {
			t.Errorf("owner missing from %s allow-list %v", name, list)
		}
	}
}

func TestEveryActorCanApprove(t *testing.T) {
	r := &Reconciler{Desired: config.DefaultDesired(), Owner: Owner{Login: "owner"}}
	got := r.WantProtection().ApprovalsWhitelistUsernames
	for _, a := range append(r.Desired.ActorNames(), "owner") {
		if !contains(got, a) {
			t.Errorf("%s missing from approvals allow-list %v", a, got)
		}
	}
	if contains(got, "manager") {
		t.Errorf("manager must not approve: %v", got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Milestone branches are pushed and merged by the manager and the owner
// only; every actor's approval counts there as on the default branch.
func TestMilestoneProtection(t *testing.T) {
	r := &Reconciler{Desired: config.DefaultDesired(), Owner: Owner{Login: "owner"}}
	p := r.WantMilestoneProtection()
	for name, list := range map[string][]string{"push": p.PushWhitelistUsernames, "merge": p.MergeWhitelistUsernames} {
		if !sameSet(list, []string{"manager", "owner"}) {
			t.Errorf("%s allow-list = %v, want the manager and the owner", name, list)
		}
	}
	if !p.EnablePush || !p.EnablePushWhitelist || !p.EnableMergeWhitelist {
		t.Errorf("milestone rule leaves pushes or merges open: %+v", p)
	}
	main := r.WantProtection()
	if !sameSet(p.ApprovalsWhitelistUsernames, main.ApprovalsWhitelistUsernames) || p.RequiredApprovals != main.RequiredApprovals {
		t.Errorf("milestone approvals = %v (%d), want the default branch's %v (%d)",
			p.ApprovalsWhitelistUsernames, p.RequiredApprovals, main.ApprovalsWhitelistUsernames, main.RequiredApprovals)
	}
	if d := ProtectionDiff(p, r.WantMilestoneProtection()); len(d) != 0 {
		t.Errorf("diff with itself = %v", d)
	}
}
