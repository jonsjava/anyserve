package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonsjava/anyserve/internal/registry"
)

func TestFetchVerifies(t *testing.T) {
	seed, pub, err := registry.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sk, _ := registry.PrivateKeyFromSeed(seed)
	dir := t.TempDir()
	bin := []byte("#!/bin/sh\necho new\n")
	sum := sha256.Sum256(bin)
	asset := AssetName("v1.2.3", "linux", "amd64")
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n")
	os.WriteFile(filepath.Join(dir, asset), bin, 0o644)
	os.WriteFile(filepath.Join(dir, "SHA256SUMS-v1.2.3"), sums, 0o644)
	os.WriteFile(filepath.Join(dir, "SHA256SUMS-v1.2.3.sig"), registry.Sign(sums, sk), 0o644)
	r := Release{Version: "v1.2.3", Base: "file://" + dir + "/"}
	got, err := Fetch(context.Background(), r, "linux", "amd64", pub)
	if err != nil || string(got) != string(bin) {
		t.Fatalf("fetch: %q %v", got, err)
	}
	if _, err := Fetch(context.Background(), r, "linux", "amd64", ReleasePublicKey); err == nil {
		t.Error("accepted checksums signed by another key")
	}
	if _, err := Fetch(context.Background(), r, "darwin", "arm64", pub); err == nil {
		t.Error("found a build that isn't there")
	}
	os.WriteFile(filepath.Join(dir, asset), append(bin, 'x'), 0o644)
	if _, err := Fetch(context.Background(), r, "linux", "amd64", pub); err == nil {
		t.Error("accepted a binary that doesn't match its checksum")
	}
	exe := filepath.Join(dir, "anyserve")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Replace(exe, bin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(bin) {
		t.Error("not replaced")
	}
}
