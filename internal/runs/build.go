package runs

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/robertolupi/bottown/internal/secrets"
)

// LauncherBuild is this binary's build, for the records of the runs it
// launches; nil when it has no VCS information. version is the -ldflags
// version (cli.Version), the fallback: the Makefile builds with
// -buildvcs=false, so an installed bottown has no VCS stamp, but its
// version is git describe --always --dirty.
func LauncherBuild(version string) *secrets.Build {
	info, _ := debug.ReadBuildInfo()
	return buildOf(info, version)
}

func buildOf(info *debug.BuildInfo, version string) *secrets.Build {
	if info != nil {
		var b secrets.Build
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Revision = s.Value
			case "vcs.modified":
				b.Dirty = s.Value == "true"
			}
		}
		if b.Revision != "" {
			return &b
		}
	}
	rev, dirty := strings.CutSuffix(version, "-dirty")
	if rev == "" || rev == "dev" || ValidBuild(&secrets.Build{Revision: rev}) != nil {
		return nil
	}
	return &secrets.Build{Revision: rev, Dirty: dirty}
}

// ValidBuild refuses a build whose revision is not a plain token: the
// server records what a launcher reports. A nil build is valid.
func ValidBuild(b *secrets.Build) error {
	if b == nil {
		return nil
	}
	if b.Revision == "" || strings.ContainsFunc(b.Revision, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("build revision %q is empty or contains spaces or control characters", b.Revision)
	}
	if len(b.Revision) > 100 {
		return fmt.Errorf("build revision %q is too long", b.Revision[:40]+"…")
	}
	return nil
}
