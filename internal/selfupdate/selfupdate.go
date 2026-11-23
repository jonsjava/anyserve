// Package selfupdate replaces the running Anyserve binary with a signed
// release.
//
// Every release publishes SHA256SUMS-<version> and a signature of it made
// with the release key, whose public half is built in here. Upgrading
// checks the signature, then the binary's checksum, then swaps the file
// atomically, so a tampered or half-downloaded binary is never run.
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jonsjava/anyserve/internal/registry"
	"github.com/jonsjava/anyserve/internal/version"
)

// ReleasePublicKey verifies release checksums.
const ReleasePublicKey = "anyserve-ed25519:dAUFcHwaJokGxbvj5dQZCu6Ise6TFdiMNSGlHNreXNo="

// Repo is where releases are published.
const Repo = "jonsjava/anyserve"

// MaxBinary is the largest binary accepted.
const MaxBinary = 256 << 20

// Release is one published version and where its files are.
type Release struct {
	Version string
	// Base is the folder URL holding the release's files.
	Base string
}

// AssetName is the binary's file name for an OS and architecture.
func AssetName(ver, goos, goarch string) string {
	n := fmt.Sprintf("anyserve-%s-%s-%s", ver, goos, goarch)
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

var client = &http.Client{Timeout: 10 * time.Minute}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context) (Release, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+Repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "anyserve")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, errors.New("no releases are published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("asking GitHub for the latest release: %s", resp.Status)
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return Release{}, err
	}
	if !version.Valid(r.Tag) {
		return Release{}, fmt.Errorf("the latest release has an odd tag %q", r.Tag)
	}
	return Release{Version: r.Tag, Base: "https://github.com/" + Repo + "/releases/download/" + r.Tag + "/"}, nil
}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	if p, ok := strings.CutPrefix(url, "file://"); ok {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, limit))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "anyserve")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is too large", url)
	}
	return b, err
}

// Fetch downloads and verifies the binary for goos/goarch from a release.
func Fetch(ctx context.Context, r Release, goos, goarch, pubKey string) ([]byte, error) {
	pub, err := registry.ParsePublicKey(pubKey)
	if err != nil {
		return nil, err
	}
	sumsName := "SHA256SUMS-" + r.Version
	sums, err := get(ctx, r.Base+sumsName, 1<<20)
	if err != nil {
		return nil, err
	}
	sig, err := get(ctx, r.Base+sumsName+".sig", 4096)
	if err != nil {
		return nil, fmt.Errorf("the release has no signature: %w", err)
	}
	if err := registry.Verify(sums, sig, pub); err != nil {
		return nil, errors.New("the release's checksums aren't signed by Anyserve's release key; refusing to install it")
	}
	asset := AssetName(r.Version, goos, goarch)
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("release %s has no build for %s/%s", r.Version, goos, goarch)
	}
	bin, err := get(ctx, r.Base+asset, MaxBinary)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(bin)
	if hex.EncodeToString(sum[:]) != want {
		return nil, errors.New("the downloaded binary doesn't match the signed checksum; refusing to install it")
	}
	return bin, nil
}

// Replace swaps the binary at path for bin, atomically.
func Replace(path string, bin []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".anyserve-upgrade-*")
	if err != nil {
		return fmt.Errorf("can't write next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// A running .exe can't be replaced, but it can be renamed.
		old := path + ".old"
		os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}

// Newer reports whether release version v is newer than the running one.
func Newer(v string) bool {
	cur := version.Current()
	if !version.Valid(cur) {
		return true // development builds can always move to a release
	}
	return version.Satisfies(v, cur) && strings.TrimPrefix(v, "v") != strings.TrimPrefix(cur, "v")
}
