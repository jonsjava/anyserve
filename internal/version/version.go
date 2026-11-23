// Package version holds Anyserve's own version and compares versions.
package version

import (
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
)

// Version is set at build time with
// -ldflags "-X github.com/jonsjava/anyserve/internal/version.Version=v1.2.3".
// Without it, Current falls back to the module version Go recorded (what
// `go install ...@v1.2.3` builds have).
var Version = ""

// Current returns this build's version, or "dev" when it isn't known.
func Current() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

var releaseRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)
var leadingRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// Valid reports whether v is a release version like 1.2.3 or v1.2.3.
func Valid(v string) bool { return releaseRe.MatchString(strings.TrimSpace(v)) }

// Satisfies reports whether a build at version have can use something that
// requires version need. Development builds, and Go pseudo-versions from a
// repository with no release yet (v0.0.0-...), satisfy everything: they're
// built from the newest source. A pseudo-version after a release
// (v1.2.4-0.2026...-abcdef) counts as that release's next patch.
func Satisfies(have, need string) bool {
	n := releaseRe.FindStringSubmatch(strings.TrimSpace(need))
	if n == nil {
		return false
	}
	have = strings.TrimSpace(have)
	if have == "" || have == "dev" || strings.HasPrefix(have, "v0.0.0-") {
		return true
	}
	h := leadingRe.FindStringSubmatch(have)
	if h == nil {
		return true
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.Atoi(h[i])
		b, _ := strconv.Atoi(n[i])
		if a != b {
			return a > b
		}
	}
	return true
}
