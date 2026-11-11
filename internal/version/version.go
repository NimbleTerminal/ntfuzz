package version

import (
	"runtime/debug"
	"strings"
)

// Version is the build-time version string, overridable via -ldflags.
var Version = "dev"

// Current returns the effective ntfuzz version, falling back to Go module build
// info when installed via `go install ...@vX.Y.Z` and then to "dev".
func Current() string {
	if v := strings.TrimSpace(Version); v != "" && v != "dev" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
