// Package moxvar provides the version number of a mox build.
package moxvar

import (
	"runtime"
	"runtime/debug"
)

// Version is set at runtime based on the Go module used to build. For builds as a
// distribution (see Distribution), it starts with the product name and release,
// followed by the mox version, e.g. "muxx 202611191730 (mox v0.0.18-...)".
var Version string

// baseVersion is the mox version, without distribution.
var baseVersion string

// VersionBare does not add a "+modifications", goversion or other suffix to the version.
var VersionBare string

func init() {
	// A distribution with an invalid name would make invalid headers at runtime.
	if err := checkDistribution(Distribution); err != nil {
		panic(err)
	}
	setBaseVersion()
	baseVersion = Version
	Version = withDistribution(Distribution, baseVersion)
}

func setBaseVersion() {
	Version = "(devel)"
	VersionBare = "(devel)"

	defer func() {
		Version += "-" + runtime.Version()
	}()

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	Version = buildInfo.Main.Version
	VersionBare = buildInfo.Main.Version
	if Version == "(devel)" {
		var vcsRev, vcsMod string
		for _, setting := range buildInfo.Settings {
			if setting.Key == "vcs.revision" {
				vcsRev = setting.Value
			} else if setting.Key == "vcs.modified" {
				vcsMod = setting.Value
			}
		}
		if vcsRev == "" {
			return
		}
		Version = vcsRev
		VersionBare = vcsRev
		switch vcsMod {
		case "false":
		case "true":
			Version += "+modifications"
		default:
			Version += "+unknown"
		}
	}
}
