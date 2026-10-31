package provision

import (
	"testing"

	"github.com/robertolupi/bottown/internal/config"
)

func TestWantProtectionHasNoDiffWithItself(t *testing.T) {
	r := &Reconciler{Desired: config.DefaultDesired(), Owner: Owner{Login: "owner"}}
	want := r.WantProtection("main")
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
	p := r.WantProtection("main")
	for name, list := range map[string][]string{
		"push": p.PushWhitelistUsernames, "merge": p.MergeWhitelistUsernames, "approvals": p.ApprovalsWhitelistUsers,
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
