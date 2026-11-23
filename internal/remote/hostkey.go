package remote

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// HostKeyPath is where the SSH server's private host key lives.
func HostKeyPath(configDir string) string {
	return filepath.Join(configDir, "remote", "ssh_host_ed25519_key")
}

// LoadOrCreateHostKey reads the host key, creating an ed25519 key the
// first time.
func LoadOrCreateHostKey(configDir string) (ssh.Signer, bool, error) {
	p := HostKeyPath(configDir)
	b, err := os.ReadFile(p)
	if err == nil {
		s, err := ssh.ParsePrivateKey(b)
		return s, false, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "anyserve host key")
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, false, err
	}
	s, err := ssh.NewSignerFromKey(priv)
	return s, true, err
}

// Fingerprint is the host key's SHA256 fingerprint, as ssh shows it.
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

func pemEncode(b *pem.Block) []byte { return pem.EncodeToMemory(b) }

func loadOrCreate(p, comment string) (ssh.Signer, error) {
	if b, err := os.ReadFile(p); err == nil {
		return ssh.ParsePrivateKey(b)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}
