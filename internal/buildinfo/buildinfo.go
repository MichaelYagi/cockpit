// Package buildinfo reports the version of the running binary.
package buildinfo

import (
	"runtime"
	"runtime/debug"
)

// Version is set at build time with
// -ldflags "-X github.com/MichaelYagi/cockpit/internal/buildinfo.Version=v0.1.0".
var Version = "dev"

// APIVersion is the API major version this binary serves.
const APIVersion = "v1"

// Info describes the build.
type Info struct {
	Version    string
	Commit     string
	Modified   bool
	GoVersion  string
	APIVersion string
}

// Get returns build information, reading VCS details embedded by the Go
// toolchain when available.
func Get() Info {
	info := Info{Version: Version, GoVersion: runtime.Version(), APIVersion: APIVersion}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Commit = s.Value
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	return info
}
