// Command relsign signs a release's checksum file with the release key, so
// `anyserve upgrade` and the installer can trust it.
//
//	ANYSERVE_RELEASE_KEY=... go run ./tools/relsign dist/SHA256SUMS-v1.2.3
//	go run ./tools/relsign -key ~/.config/anyserve-dev/release-signing.key FILE
//
// It writes FILE.sig and checks it against the public key built into
// Anyserve, so a release can't be signed with the wrong key by mistake.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jonsjava/anyserve/internal/registry"
	"github.com/jonsjava/anyserve/internal/selfupdate"
)

func main() {
	keyPath := flag.String("key", "", "private key file (or set ANYSERVE_RELEASE_KEY)")
	flag.Parse()
	if err := run(*keyPath, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}
}

func run(keyPath string, files []string) error {
	if len(files) != 1 {
		return fmt.Errorf("give one file to sign")
	}
	keyText := os.Getenv("ANYSERVE_RELEASE_KEY")
	if keyText == "" {
		if keyPath == "" {
			return fmt.Errorf("no key: pass -key or set ANYSERVE_RELEASE_KEY")
		}
		b, err := os.ReadFile(keyPath)
		if err != nil {
			return err
		}
		keyText = string(b)
	}
	sk, err := registry.PrivateKeyFromSeed(keyText)
	if err != nil {
		return err
	}
	if got := registry.PublicKeyString(sk); got != selfupdate.ReleasePublicKey {
		return fmt.Errorf("this key's public half is %s, but Anyserve trusts %s", got, selfupdate.ReleasePublicKey)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		return err
	}
	return os.WriteFile(files[0]+".sig", registry.Sign(data, sk), 0o644)
}
