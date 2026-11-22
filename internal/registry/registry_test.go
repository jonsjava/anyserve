package registry

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildSigned(t *testing.T) (dir string, src Source, index, sig []byte) {
	t.Helper()
	ix, err := Build("test", "../../registry/services", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	index, err = ix.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	seed, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sk, err := PrivateKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	if PublicKeyString(sk) != pub {
		t.Fatal("public key mismatch")
	}
	sig = Sign(index, sk)
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.json"), index, 0o644)
	os.WriteFile(filepath.Join(dir, "index.json.sig"), sig, 0o644)
	return dir, Source{Name: "test", URL: filepath.Join(dir, "index.json"), PublicKey: pub}, index, sig
}

func TestBuildSignFetchRoundTrip(t *testing.T) {
	_, src, _, _ := buildSigned(t)
	index, sig, err := Fetch(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := VerifyAndParse(src, index, sig)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rust", "terraria", "wurm-unlimited"} {
		d, ok := ix.Find(id)
		if !ok {
			t.Fatalf("missing %s", id)
		}
		// Defaults must survive the JSON round trip.
		if _, err := d.ResolveSettings(map[string]string{}); err != nil {
			t.Errorf("%s after round trip: %v", id, err)
		}
	}
	rust, _ := ix.Find("rust")
	s, _ := rust.Setting("seed")
	if s.DefaultString() != "1337" {
		t.Errorf("seed default = %q", s.DefaultString())
	}
}

func TestTamperedIndexRejected(t *testing.T) {
	dir, src, index, _ := buildSigned(t)
	bad := bytes.Replace(index, []byte(`"app_id": 258550`), []byte(`"app_id": 1`), 1)
	if bytes.Equal(bad, index) {
		t.Fatal("tamper had no effect")
	}
	os.WriteFile(filepath.Join(dir, "index.json"), bad, 0o644)
	if _, _, err := Fetch(context.Background(), src); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("want bad signature, got %v", err)
	}
}

func TestWrongKeyRejected(t *testing.T) {
	_, src, _, _ := buildSigned(t)
	_, other, _ := GenerateKey()
	src.PublicKey = other
	if _, _, err := Fetch(context.Background(), src); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("want bad signature, got %v", err)
	}
}

func TestCacheReverifies(t *testing.T) {
	_, src, index, sig := buildSigned(t)
	c := Cache{Dir: t.TempDir()}
	if _, err := c.Load(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty cache: %v", err)
	}
	if err := c.Save(src.Name, index, sig); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(src); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(c.Dir, src.Name, "index.json")
	os.WriteFile(p, append(index, ' '), 0o644)
	if _, err := c.Load(src); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered cache: %v", err)
	}
}

func TestPlainHTTPRefused(t *testing.T) {
	_, src, _, _ := buildSigned(t)
	src.URL = "http://example.com/index.json"
	if _, _, err := Fetch(context.Background(), src); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("want https error, got %v", err)
	}
}

func TestEmbeddedSnapshotVerifies(t *testing.T) {
	ix, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Services) == 0 {
		t.Fatal("embedded index is empty")
	}
}
