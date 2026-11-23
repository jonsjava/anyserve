package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
)

// FleetConfig is this machine's place in a fleet: as a manager, the join
// tokens it has handed out and the nodes that joined; as a worker, the
// manager it belongs to.
type FleetConfig struct {
	// Address is how workers reach this manager's SSH server (host:port),
	// when it can't be worked out from the listen address.
	Address string       `json:"address,omitempty"`
	Tokens  []FleetToken `json:"tokens,omitempty"`
	Nodes   []Node       `json:"nodes,omitempty"`
	Manager *ManagerLink `json:"manager,omitempty"`
}

// FleetToken is a single-use join token, stored as a hash.
type FleetToken struct {
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
}

// Node is a worker that joined this manager.
type Node struct {
	Name string `json:"name"`
	// Key is the node's SSH public key (authorized_keys format); the node
	// signs in with it.
	Key    string    `json:"key"`
	Joined time.Time `json:"joined"`
}

// ManagerLink is the manager a worker belongs to.
type ManagerLink struct {
	Addr string `json:"addr"`
	// HostKey is the manager's SSH host key (authorized_keys format),
	// pinned from the join token.
	HostKey string `json:"host_key"`
	// Name is this node's name on the manager.
	Name string `json:"name"`
}

// TokenTTL is how long a join token works.
const TokenTTL = time.Hour

var nodeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidNodeName checks a node name such as "box-2".
func ValidNodeName(n string) error {
	if !nodeNameRe.MatchString(n) {
		return fmt.Errorf("node name %q must be lowercase letters, digits and dashes", n)
	}
	return nil
}

func hashToken(secret string) string {
	s := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(s[:])
}

// NewJoinToken creates a single-use token. The token handed out is the
// secret followed by the manager's host key fingerprint, so a joining
// worker knows which manager to trust.
func (a *App) NewJoinToken(hostKeyFingerprint string) (string, time.Time, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	cfg, err := a.LoadConfig()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	exp := now.Add(TokenTTL)
	// Drop expired tokens while we're here.
	cfg.Fleet.Tokens = slices.DeleteFunc(cfg.Fleet.Tokens, func(t FleetToken) bool { return now.After(t.Expires) })
	cfg.Fleet.Tokens = append(cfg.Fleet.Tokens, FleetToken{Hash: hashToken(secret), Expires: exp})
	if err := a.SaveConfig(cfg); err != nil {
		return "", time.Time{}, err
	}
	fp := strings.TrimPrefix(hostKeyFingerprint, "SHA256:")
	return secret + "." + fp, exp, nil
}

// SplitJoinToken separates a token into its secret and the manager's host
// key fingerprint (SHA256:...).
func SplitJoinToken(tok string) (secret, fingerprint string, err error) {
	secret, fp, ok := strings.Cut(strings.TrimSpace(tok), ".")
	if !ok || len(secret) < 20 || fp == "" {
		return "", "", errors.New("that isn't a join token; copy the whole command from `anyserve fleet token`")
	}
	return secret, "SHA256:" + fp, nil
}

// ErrBadToken means a join token is unknown, used or expired.
var ErrBadToken = errors.New("the join token is wrong, already used or expired; make a new one with `anyserve fleet token`")

// Enroll uses up a join token and records a new node.
func (a *App) Enroll(secret, name, key string) error {
	if err := ValidNodeName(name); err != nil {
		return err
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	h := hashToken(secret)
	now := time.Now().UTC()
	idx := -1
	for i, t := range cfg.Fleet.Tokens {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) == 1 && now.Before(t.Expires) {
			idx = i
		}
	}
	if idx < 0 {
		return ErrBadToken
	}
	cfg.Fleet.Tokens = slices.Delete(cfg.Fleet.Tokens, idx, idx+1)
	for _, n := range cfg.Fleet.Nodes {
		if n.Name == name {
			// The token is spent either way, so a guessed name can't be
			// retried with it.
			a.SaveConfig(cfg)
			return fmt.Errorf("a node called %s already exists; join with another name (--name), or remove the old one on the manager", name)
		}
	}
	cfg.Fleet.Nodes = append(cfg.Fleet.Nodes, Node{Name: name, Key: key, Joined: now})
	return a.SaveConfig(cfg)
}

// RemoveNode forgets a node; it can't sign in again.
func (a *App) RemoveNode(name string) error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	n := len(cfg.Fleet.Nodes)
	cfg.Fleet.Nodes = slices.DeleteFunc(cfg.Fleet.Nodes, func(x Node) bool { return x.Name == name })
	if len(cfg.Fleet.Nodes) == n {
		return fmt.Errorf("no node called %s", name)
	}
	return a.SaveConfig(cfg)
}

// NodeForKey finds the node that signs in with key (authorized_keys form).
func (c *Config) NodeForKey(key string) (Node, bool) {
	for _, n := range c.Fleet.Nodes {
		if n.Key == key {
			return n, true
		}
	}
	return Node{}, false
}

// ManagerAddr is the address workers should dial: the configured one, or
// the SSH server's listen address with this machine's address filled in.
func (a *App) ManagerAddr() (string, error) {
	cfg, err := a.LoadConfig()
	if err != nil {
		return "", err
	}
	if cfg.Fleet.Address != "" {
		return cfg.Fleet.Address, nil
	}
	if !cfg.Remote.Enabled {
		return "", errors.New("remote access is off, so workers can't connect: `anyserve remote enable --listen 0.0.0.0:7022`")
	}
	listen := cfg.Remote.Listen
	if listen == "" {
		listen = DefaultRemoteListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(host)
	if host == "localhost" || ip != nil && ip.IsLoopback() {
		return "", errors.New("the SSH server listens on this machine only, so workers can't reach it: `anyserve remote enable --listen 0.0.0.0:7022` (and open TCP " + port + " in the firewall)")
	}
	if host != "" && (ip == nil || !ip.IsUnspecified()) {
		return listen, nil
	}
	// Listening everywhere: offer this machine's own address, public first.
	v4, v6 := publicAddrs()
	if len(v4) > 0 {
		return net.JoinHostPort(v4[0], port), nil
	}
	if p := privateAddr(); p != "" {
		return net.JoinHostPort(p, port), nil
	}
	if len(v6) > 0 {
		return net.JoinHostPort(v6[0], port), nil
	}
	return "", errors.New("can't tell this machine's address; give it with `anyserve fleet token --addr host:port`")
}

// SetManagerAddr records how workers reach this manager ("" to work it out).
func (a *App) SetManagerAddr(addr string) error {
	if addr != "" {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return fmt.Errorf("give the address as host:port: %w", err)
		}
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Fleet.Address = addr
	return a.SaveConfig(cfg)
}

func privateAddr() string {
	ifs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, x := range ifs {
		if ipn, ok := x.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
			return ipn.IP.String()
		}
	}
	return ""
}
