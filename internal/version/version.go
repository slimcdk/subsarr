// Package version reports which build is running.
package version

import "runtime/debug"

// Version is set at build time from the release tag:
//
//	-ldflags "-X github.com/slimcdk/subsarr/internal/version.Version=v1.1.0"
//
// A build without one falls back to the module's own recorded version, so /info
// still identifies something rather than claiming to be a release it is not.
var Version = ""

func String() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
				return "dev-" + setting.Value[:7]
			}
		}
	}
	return "dev"
}
