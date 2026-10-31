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
