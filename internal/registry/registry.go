// Package registry builds, signs, fetches and verifies registry indexes.
//
// A registry is a git repo of service definitions. CI turns it into a single
// index.json plus index.json.sig, an Ed25519 signature over the exact bytes
// of index.json. Clients only trust an index whose signature checks out
// against a public key they already have, so a compromised web host can't
// push definitions to users.
package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonsjava/anyserve/internal/spec"
)

// IndexSchema is the index format version.
const IndexSchema = 1

// KeyPrefix marks an Anyserve public key string.
const KeyPrefix = "anyserve-ed25519:"

// maxIndexSize caps downloads so a hostile server can't exhaust memory.
const maxIndexSize = 16 << 20

// Index is the published registry index.
type Index struct {
	Schema    int                `json:"schema"`
	Name      string             `json:"name"`
	Generated time.Time          `json:"generated"`
	Services  []*spec.Definition `json:"services"`
}

// Find returns the service with the given id.
func (ix *Index) Find(id string) (*spec.Definition, bool) {
	for _, d := range ix.Services {
		if d.ID == id {
			return d, true
		}
	}
	return nil, false
}

// Build reads every *.yaml definition in dir, validates them all and returns
// the index. It reports every invalid file, not just the first.
func Build(name, dir string, now time.Time) (*Index, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no *.yaml definitions in %s", dir)
	}
	sort.Strings(files)
	ix := &Index{Schema: IndexSchema, Name: name, Generated: now.UTC().Truncate(time.Second)}
	var errs []string
	seen := map[string]string{}
	for _, f := range files {
		d, err := spec.LoadFile(f)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if want := strings.TrimSuffix(filepath.Base(f), ".yaml"); d.ID != want {
			errs = append(errs, fmt.Sprintf("%s: id %q must match the file name", f, d.ID))
			continue
		}
		if prev, dup := seen[d.ID]; dup {
			errs = append(errs, fmt.Sprintf("%s: id %q already used by %s", f, d.ID, prev))
			continue
		}
		seen[d.ID] = f
		ix.Services = append(ix.Services, d)
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "\n"))
	}
	return ix, nil
}

// Marshal encodes an index deterministically.
func (ix *Index) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseIndex decodes and checks an index. Call it only on verified bytes.
func ParseIndex(data []byte) (*Index, error) {
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	if ix.Schema != IndexSchema {
		return nil, fmt.Errorf("index schema %d is not supported by this version of Anyserve (want %d); try updating Anyserve", ix.Schema, IndexSchema)
	}
	for _, d := range ix.Services {
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("index service %q: %w", d.ID, err)
		}
	}
	return &ix, nil
}

// GenerateKey creates a signing key pair. The private key is returned as a
// base64 seed for storing in a file or CI secret; the public key in the
// "anyserve-ed25519:..." form clients pin.
func GenerateKey() (privSeed, pub string, err error) {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(sk.Seed()), KeyPrefix + base64.StdEncoding.EncodeToString(pk), nil
}

// PrivateKeyFromSeed decodes a base64 seed produced by GenerateKey.
func PrivateKeyFromSeed(s string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("private key must be a base64 Ed25519 seed (create one with `go run ./tools/regbuild -keygen -key <file>`)")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// PublicKeyString renders a private key's public half for pinning.
func PublicKeyString(sk ed25519.PrivateKey) string {
	return KeyPrefix + base64.StdEncoding.EncodeToString(sk.Public().(ed25519.PublicKey))
}

// ParsePublicKey decodes an "anyserve-ed25519:..." public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, KeyPrefix) {
		return nil, fmt.Errorf("public key must start with %q", KeyPrefix)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, KeyPrefix))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("public key is not a valid Ed25519 key")
	}
	return ed25519.PublicKey(b), nil
}

// Sign returns the signature file contents for index bytes.
func Sign(index []byte, sk ed25519.PrivateKey) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(sk, index)) + "\n")
}

// ErrBadSignature means the index was not signed by a trusted key.
var ErrBadSignature = errors.New("registry index signature does not match the trusted key; refusing to use it")

// Verify checks index bytes against a signature file and a trusted key.
func Verify(index, sigFile []byte, pub ed25519.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, index, sig) {
		return ErrBadSignature
	}
	return nil
}

// Source is one configured registry.
type Source struct {
	Name string `json:"name"`
	// URL of index.json. The signature is fetched from URL + ".sig".
	// file:// URLs and plain paths work for local testing.
	URL       string `json:"url"`
	PublicKey string `json:"public_key"`
}

// Fetch downloads and verifies a source's index, returning the raw verified
// bytes and signature so the caller can cache them.
func Fetch(ctx context.Context, src Source) (index, sig []byte, err error) {
	pub, err := ParsePublicKey(src.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("registry %s: %w", src.Name, err)
	}
	index, err = get(ctx, src.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("registry %s: %w", src.Name, err)
	}
	sig, err = get(ctx, src.URL+".sig")
	if err != nil {
		return nil, nil, fmt.Errorf("registry %s: signature: %w", src.Name, err)
	}
	if err := Verify(index, sig, pub); err != nil {
		return nil, nil, fmt.Errorf("registry %s: %w", src.Name, err)
	}
	if _, err := ParseIndex(index); err != nil {
		return nil, nil, fmt.Errorf("registry %s: %w", src.Name, err)
	}
	return index, sig, nil
}

func get(ctx context.Context, url string) ([]byte, error) {
	if p, ok := localPath(url); ok {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return readLimited(f)
	}
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("%s: registries must use https:// (or a local path)", url)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "anyserve")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", url, resp.Status)
	}
	return readLimited(resp.Body)
}

func readLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxIndexSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxIndexSize {
		return nil, fmt.Errorf("index is larger than %d MB", maxIndexSize>>20)
	}
	return b, nil
}

func localPath(url string) (string, bool) {
	if strings.HasPrefix(url, "file://") {
		return strings.TrimPrefix(url, "file://"), true
	}
	if strings.Contains(url, "://") {
		return "", false
	}
	return url, true
}

// Cache stores verified indexes on disk, one directory per registry.
type Cache struct{ Dir string }

func (c Cache) paths(name string) (string, string) {
	d := filepath.Join(c.Dir, name)
	return filepath.Join(d, "index.json"), filepath.Join(d, "index.json.sig")
}

// Save writes verified index bytes atomically.
func (c Cache) Save(name string, index, sig []byte) error {
	ip, sp := c.paths(name)
	if err := os.MkdirAll(filepath.Dir(ip), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(sp, sig); err != nil {
		return err
	}
	return writeAtomic(ip, index)
}

// Load reads a cached index and verifies it again, so a tampered cache file
// is caught too. It returns os.ErrNotExist when nothing is cached.
func (c Cache) Load(src Source) (*Index, error) {
	ip, sp := c.paths(src.Name)
	index, err := os.ReadFile(ip)
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(sp)
	if err != nil {
		return nil, err
	}
	return VerifyAndParse(src, index, sig)
}

// VerifyAndParse verifies index bytes for a source and decodes them.
func VerifyAndParse(src Source, index, sig []byte) (*Index, error) {
	pub, err := ParsePublicKey(src.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", src.Name, err)
	}
	if err := Verify(index, sig, pub); err != nil {
		return nil, fmt.Errorf("registry %s: %w", src.Name, err)
	}
	return ParseIndex(index)
}

func writeAtomic(p string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
