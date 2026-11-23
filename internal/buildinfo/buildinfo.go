// Package buildinfo says which build is running. The release build stamps
// Version, Commit and BuiltAt with -ldflags -X (see `make release`); a plain
// `go run` leaves them empty and Commit and BuiltAt fall back to what the Go
// toolchain recorded.
package buildinfo

import "runtime/debug"

// Set at link time.
var (
	Version = "" // release tag, e.g. r12
	Commit  = ""
	BuiltAt = "" // RFC 3339
)

// Info is the running build.
type Info struct {
	Version string
	Commit  string
	BuiltAt string
}

// Get returns the stamped values, filling gaps from the embedded VCS data.
func Get() Info {
	info := Info{Version: Version, Commit: Commit, BuiltAt: BuiltAt}
	if info.Version == "" {
		info.Version = "dev"
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch {
			case s.Key == "vcs.revision" && info.Commit == "":
				info.Commit = s.Value
			case s.Key == "vcs.time" && info.BuiltAt == "":
				info.BuiltAt = s.Value
			}
		}
	}
	return info
}
