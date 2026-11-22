// Command regbuild builds, signs and verifies registry indexes.
// `make registry` runs it to refresh the snapshot embedded in the anyserve
// binary, and `make publish-registry` to publish the main registry.
//
//	regbuild -keygen -key path/to/new.key
//	regbuild -src registry/services -out internal/registry/snapshot -key path/to.key
//	regbuild -verify path/to/index.json [-pub anyserve-ed25519:...]
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonsjava/anyserve/internal/registry"
)

func main() {
	var (
		keygen  = flag.Bool("keygen", false, "create a new signing key at -key and print its public key")
		keyPath = flag.String("key", os.Getenv("ANYSERVE_REGISTRY_KEY_FILE"), "private key file (or set ANYSERVE_REGISTRY_KEY for the key itself)")
		src     = flag.String("src", "registry/services", "directory of *.yaml definitions")
		out     = flag.String("out", "dist/registry", "output directory for index.json and index.json.sig")
		name    = flag.String("name", registry.MainName, "registry name")
		check   = flag.Bool("check", false, "only validate the definitions; don't write anything")
		verify  = flag.String("verify", "", "check that this index.json matches its .sig file and the -pub key, then exit")
		pub     = flag.String("pub", registry.MainPublicKey, "public key for -verify")
	)
	flag.Parse()
	if *verify != "" {
		if err := verifyIndex(*verify, *pub, *name); err != nil {
			fmt.Fprintln(os.Stderr, "regbuild:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*keygen, *keyPath, *src, *out, *name, *check); err != nil {
		fmt.Fprintln(os.Stderr, "regbuild:", err)
		os.Exit(1)
	}
}

func run(keygen bool, keyPath, src, out, name string, check bool) error {
	if keygen {
		if keyPath == "" {
			return fmt.Errorf("-keygen needs -key")
		}
		if _, err := os.Stat(keyPath); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite a signing key", keyPath)
		}
		seed, pub, err := registry.GenerateKey()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(keyPath, []byte(seed+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println(pub)
		return nil
	}

	ix, err := registry.Build(name, src, time.Now())
	if err != nil {
		return err
	}
	if check {
		fmt.Printf("%d definitions are valid\n", len(ix.Services))
		return nil
	}
	keyText := os.Getenv("ANYSERVE_REGISTRY_KEY")
	if keyText == "" {
		if keyPath == "" {
			return fmt.Errorf("no signing key: pass -key or set ANYSERVE_REGISTRY_KEY")
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
	data, err := ix.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "index.json"), data, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "index.json.sig"), registry.Sign(data, sk), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d services to %s (signed by %s)\n", len(ix.Services), out, registry.PublicKeyString(sk))
	return nil
}

func verifyIndex(path, pub, name string) error {
	index, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return err
	}
	ix, err := registry.VerifyAndParse(registry.Source{Name: name, PublicKey: pub}, index, sig)
	if err != nil {
		return err
	}
	fmt.Printf("%s: signature good, %d services, generated %s\n", path, len(ix.Services), ix.Generated)
	return nil
}
