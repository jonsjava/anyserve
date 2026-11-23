package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// Host is a remote Anyserve host as the client remembers it.
type Host struct {
	Name string `json:"name"`
	// Addr is host:port of the remote's SSH server.
	Addr string `json:"addr"`
	User string `json:"user"`
	// Key is a private key file; empty means use ssh-agent.
	Key string `json:"key,omitempty"`
	// HostKey is the server's public key (authorized_keys format), pinned
	// when the host was added.
	HostKey string `json:"host_key"`
}

// ErrHostKeyChanged means the server's key isn't the one pinned.
var ErrHostKeyChanged = errors.New("the host's key has changed")

func authMethods(keyFile string) ([]ssh.AuthMethod, func(), error) {
	cleanup := func() {}
	if keyFile == "" {
		sock := os.Getenv("SSH_AUTH_SOCK")
		if sock == "" {
			return nil, cleanup, errors.New("no key given and no ssh-agent running; pass --key ~/.ssh/id_ed25519")
		}
		c, err := net.Dial("unix", sock)
		if err != nil {
			return nil, cleanup, fmt.Errorf("ssh-agent: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(c).Signers)}, func() { c.Close() }, nil
	}
	b, err := os.ReadFile(expand(keyFile))
	if err != nil {
		return nil, cleanup, err
	}
	signer, err := ssh.ParsePrivateKey(b)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, cleanup, fmt.Errorf("%s needs a passphrase; load it into ssh-agent instead", keyFile)
		}
		fmt.Fprintf(os.Stderr, "Passphrase for %s: ", keyFile)
		pass, perr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if perr != nil {
			return nil, cleanup, perr
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(b, pass)
	}
	if err != nil {
		return nil, cleanup, fmt.Errorf("%s: %w", keyFile, err)
	}
	signers := []ssh.Signer{}
	// An admin certificate beside the key (id_ed25519-cert.pub, as
	// OpenSSH names it) is offered first.
	if cb, err := os.ReadFile(expand(keyFile) + "-cert.pub"); err == nil {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey(cb); err == nil {
			if cert, ok := pk.(*ssh.Certificate); ok {
				if cs, err := ssh.NewCertSigner(cert, signer); err == nil {
					signers = append(signers, cs)
				}
			}
		}
	}
	signers = append(signers, signer)
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, cleanup, nil
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// Dial connects to a host, checking its key against the pinned one.
func Dial(h Host) (*ssh.Client, error) {
	pinned, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.HostKey))
	if err != nil {
		return nil, fmt.Errorf("%s: no pinned host key; add the host again with `anyserve remote add`", h.Name)
	}
	return dial(h, func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if !bytes.Equal(key.Marshal(), pinned.Marshal()) {
			return fmt.Errorf("%w: expected %s, got %s. If the host was reinstalled, remove and re-add it; otherwise someone may be intercepting the connection", ErrHostKeyChanged, Fingerprint(pinned), Fingerprint(key))
		}
		return nil
	})
}

// Probe connects without a pinned key and returns the server's key, so
// the user can check its fingerprint before trusting it.
func Probe(h Host) (ssh.PublicKey, error) {
	var seen ssh.PublicKey
	c, err := dial(h, func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		seen = key
		return nil
	})
	if c != nil {
		c.Close()
	}
	if seen == nil {
		return nil, err
	}
	// The key is known even if login then failed; say why.
	return seen, err
}

func dial(h Host, check ssh.HostKeyCallback) (*ssh.Client, error) {
	auth, cleanup, err := authMethods(h.Key)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	cfg := &ssh.ClientConfig{
		User:            h.User,
		Auth:            auth,
		HostKeyCallback: check,
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519,
		},
		Timeout: 15 * time.Second,
	}
	return ssh.Dial("tcp", h.Addr, cfg)
}

// APIClient returns an HTTP client whose connections go to the remote API
// through the SSH connection. Use it with URLs like http://api.anyserve/v1/...
func APIClient(c *ssh.Client) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return c.DialContext(ctx, "tcp", net.JoinHostPort(APIHost, strconv.Itoa(APIPort)))
		},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     time.Minute,
	}}
}

// APIURL is the base URL to use with APIClient.
const APIURL = "http://" + APIHost

// Forward listens on local and forwards each connection to port on the
// remote host, until ctx ends.
func Forward(ctx context.Context, c *ssh.Client, l net.Listener, port int, log func(string, ...any)) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		lc, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			rc, err := c.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				log("Forward to port %d: %v", port, err)
				lc.Close()
				return
			}
			pipe(lc, rc)
		}()
	}
}

// ReadAll is a small helper for API calls.
func ReadAll(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	return string(b)
}
