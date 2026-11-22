package registry

import (
	_ "embed"
)

// MainName is the name of the built-in registry.
const MainName = "main"

// MainURL is where the main registry index is published: GitHub Pages for
// github.com/jonsjava/anyserve-registry, updated with `make publish-registry`.
const MainURL = "https://jonsjava.github.io/anyserve-registry/index.json"

// MainPublicKey is the pinned key the main registry is signed with.
// Rotating it requires a new Anyserve release.
const MainPublicKey = "anyserve-ed25519:6dJrvi7OZIf0qcvUcX0ZoPv6b+RyRSFWjTtOLRrMYOE="

// The binary carries a signed snapshot of the main registry, so a fresh
// install works offline or when the registry can't be reached. Regenerate it
// with `make registry`.
//
//go:embed snapshot/index.json
var snapshotIndex []byte

//go:embed snapshot/index.json.sig
var snapshotSig []byte

// Main returns the built-in registry source.
func Main() Source {
	return Source{Name: MainName, URL: MainURL, PublicKey: MainPublicKey}
}

// Embedded returns the verified snapshot built into this binary.
func Embedded() (*Index, error) {
	return VerifyAndParse(Main(), snapshotIndex, snapshotSig)
}

// EmbeddedBytes returns the raw snapshot, for seeding the cache.
func EmbeddedBytes() (index, sig []byte) { return snapshotIndex, snapshotSig }
