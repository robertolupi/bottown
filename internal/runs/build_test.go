package runs

import (
	"runtime/debug"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/secrets"
)

func TestBuildOf(t *testing.T) {
	stamped := func(kv ...string) *debug.BuildInfo {
		info := &debug.BuildInfo{}
		for i := 0; i < len(kv); i += 2 {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: kv[i], Value: kv[i+1]})
		}
		return info
	}
	const full = "eee49e5b74dd0123456789abcdef0123456789ab"
	for _, tc := range []struct {
		name    string
		info    *debug.BuildInfo
		version string
		want    *secrets.Build
	}{
		{"stamped clean", stamped("vcs", "git", "vcs.revision", full, "vcs.modified", "false"), "dev", &secrets.Build{Revision: full}},
		{"stamped dirty", stamped("vcs.revision", full, "vcs.modified", "true"), "dev", &secrets.Build{Revision: full, Dirty: true}},
		{"stamp wins over version", stamped("vcs.revision", full), "ed1b16b48e47", &secrets.Build{Revision: full}},
		{"ldflags version", stamped("-buildvcs", "false"), "ed1b16b48e47", &secrets.Build{Revision: "ed1b16b48e47"}},
		{"ldflags version dirty", stamped(), "ed1b16b48e47-dirty", &secrets.Build{Revision: "ed1b16b48e47", Dirty: true}},
		{"no build info", nil, "ed1b16b48e47", &secrets.Build{Revision: "ed1b16b48e47"}},
		{"neither", stamped(), "dev", nil},
		{"empty version", nil, "", nil},
		{"odd version", nil, "not a revision", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildOf(tc.info, tc.version)
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Errorf("buildOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A test binary has no VCS stamp: with the default version it records no
// build, and does not fail.
func TestLauncherBuildTestBinary(t *testing.T) {
	if b := LauncherBuild("dev"); b != nil {
		t.Errorf("LauncherBuild(dev) in a test binary = %+v, want nil", b)
	}
}

func TestValidBuild(t *testing.T) {
	for _, b := range []*secrets.Build{nil, {Revision: "abc"}, {Revision: "v1.2-3-gabc", Dirty: true}} {
		if err := ValidBuild(b); err != nil {
			t.Errorf("ValidBuild(%+v) = %v", b, err)
		}
	}
	for _, b := range []*secrets.Build{{}, {Revision: "a b"}, {Revision: "a\nb"}, {Revision: strings.Repeat("a", 101)}} {
		if ValidBuild(b) == nil {
			t.Errorf("ValidBuild(%+v) = nil, want an error", b)
		}
	}
}
