package app

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
)

// User is someone who may manage instances remotely, through the built-in
// SSH server. Admins manage every instance; other users only the ones
// they've been granted.
type User struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin,omitempty"`
	// Keys are public keys in authorized_keys format.
	Keys []string `json:"keys,omitempty"`
	// Instances this user may manage (ignored for admins).
	Instances []string `json:"instances,omitempty"`
	// CertSerials are the browser client certificates issued to this user
	// that are still valid (hex serial numbers). Revoking one removes it.
	CertSerials []string `json:"cert_serials,omitempty"`
}

// RemoteConfig holds the built-in SSH server's settings.
type RemoteConfig struct {
	// Enabled turns on the SSH server in `anyserve serve`.
	Enabled bool `json:"enabled,omitempty"`
	// Listen is the address the SSH server listens on, e.g. 0.0.0.0:7022.
	Listen string `json:"listen,omitempty"`
}

// RemoteHost is another machine's Anyserve that this one manages, as
// `anyserve remote add` saved it.
type RemoteHost struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
	User string `json:"user"`
	// Key is a private key file; empty means ssh-agent.
	Key string `json:"key,omitempty"`
	// HostKey is the pinned server key, in authorized_keys format.
	HostKey string `json:"host_key"`
}

// FindHost returns a saved remote host.
func (c *Config) FindHost(name string) (*RemoteHost, error) {
	for i := range c.Hosts {
		if c.Hosts[i].Name == name {
			return &c.Hosts[i], nil
		}
	}
	return nil, fmt.Errorf("no remote host %q (see `anyserve remote list`)", name)
}

// DefaultRemoteListen is where the SSH server listens unless told
// otherwise: this machine only, so opening it to the network is a choice.
const DefaultRemoteListen = "127.0.0.1:7022"

// Caller is who's making an API request.
type Caller struct {
	Name string
	// Admin is the caller's effective admin power: the host's own user,
	// or an admin user whose browser presented a client certificate from
	// this host's web CA. Without one, an admin is an ordinary user.
	Admin bool
	// Cert is true when a remote caller's browser presented a valid
	// client certificate issued to them.
	Cert bool
	// Local is true for the host's own user, over the unix socket.
	Local bool
}

// Owner is the host's own user, who may do anything.
var Owner = Caller{Name: "owner", Admin: true, Local: true}

var userNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ErrNoUser means there's no user with that name.
var ErrNoUser = errors.New("no such user")

// FindUser returns a user from the config.
func (c *Config) FindUser(name string) (*User, error) {
	for i := range c.Users {
		if c.Users[i].Name == name {
			return &c.Users[i], nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrNoUser, name)
}

// CanManage reports whether a caller may manage an instance.
func (a *App) CanManage(c Caller, instance string) bool {
	if c.Local {
		return true
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return false
	}
	u, err := cfg.FindUser(c.Name)
	if err != nil {
		return false
	}
	// Re-check admin from the config, so a demotion applies at once, and
	// require the certificate for admin power.
	return (u.Admin && c.Cert) || slices.Contains(u.Instances, instance)
}

// CallerFor returns the caller for a configured user. cert says whether
// their browser presented a valid client certificate; admin power needs
// both the user's admin flag and the certificate.
func (a *App) CallerFor(name string, cert bool) (Caller, error) {
	cfg, err := a.LoadConfig()
	if err != nil {
		return Caller{}, err
	}
	u, err := cfg.FindUser(name)
	if err != nil {
		return Caller{}, err
	}
	return Caller{Name: u.Name, Admin: u.Admin && cert, Cert: cert}, nil
}

// AddUser creates a user with one or more public keys.
func (a *App) AddUser(name string, admin bool, keys []string) error {
	if !userNameRe.MatchString(name) || name == Owner.Name {
		return fmt.Errorf("user name %q must be 1-32 lowercase letters, digits, - and _, starting with a letter (and not %q)", name, Owner.Name)
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if _, err := cfg.FindUser(name); err == nil {
		return fmt.Errorf("user %q already exists", name)
	}
	cfg.Users = append(cfg.Users, User{Name: name, Admin: admin, Keys: keys})
	sort.Slice(cfg.Users, func(i, j int) bool { return cfg.Users[i].Name < cfg.Users[j].Name })
	return a.SaveConfig(cfg)
}

// UpdateUser changes a user with f and saves the config.
func (a *App) UpdateUser(name string, f func(u *User) error) error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	u, err := cfg.FindUser(name)
	if err != nil {
		return err
	}
	if err := f(u); err != nil {
		return err
	}
	return a.SaveConfig(cfg)
}

// RemoveUser deletes a user.
func (a *App) RemoveUser(name string) error {
	cfg, err := a.LoadConfig()
	if err != nil {
		return err
	}
	if _, err := cfg.FindUser(name); err != nil {
		return err
	}
	cfg.Users = slices.DeleteFunc(cfg.Users, func(u User) bool { return u.Name == name })
	return a.SaveConfig(cfg)
}

// Grant lets a user manage instances.
func (a *App) Grant(name string, instances ...string) error {
	for _, in := range instances {
		if !a.Store.Exists(in) {
			return fmt.Errorf("no instance %q (see `anyserve list`)", in)
		}
	}
	return a.UpdateUser(name, func(u *User) error {
		for _, in := range instances {
			if !slices.Contains(u.Instances, in) {
				u.Instances = append(u.Instances, in)
			}
		}
		sort.Strings(u.Instances)
		return nil
	})
}

// Revoke takes instances away from a user.
func (a *App) Revoke(name string, instances ...string) error {
	return a.UpdateUser(name, func(u *User) error {
		u.Instances = slices.DeleteFunc(u.Instances, func(s string) bool { return slices.Contains(instances, s) })
		return nil
	})
}

// VisibleTo filters instance names to those a caller may manage.
func (a *App) VisibleTo(c Caller, names []string) []string {
	var out []string
	for _, n := range names {
		if a.CanManage(c, n) {
			out = append(out, n)
		}
	}
	return out
}

// NormalizeKey checks a public key line (authorized_keys format) and
// returns it in a canonical form: type, key and comment.
func NormalizeKey(line string) (string, error) {
	pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return "", errors.New("not an SSH public key (expected a line like \"ssh-ed25519 AAAA... you@laptop\")")
	}
	out := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	if comment != "" {
		out += " " + comment
	}
	return out, nil
}

// UserForKey finds the user with this public key.
func (c *Config) UserForKey(key ssh.PublicKey) (*User, bool) {
	want := key.Marshal()
	for i := range c.Users {
		for _, line := range c.Users[i].Keys {
			pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
			if err == nil && bytes.Equal(pk.Marshal(), want) {
				return &c.Users[i], true
			}
		}
	}
	return nil, false
}

// IsAdmin reports whether a caller has admin power right now: the host's
// own user, or an admin user with their browser client certificate. It
// re-reads the config, so a demotion applies at once.
func (a *App) IsAdmin(c Caller) bool {
	if c.Local {
		return true
	}
	if !c.Cert {
		return false
	}
	cfg, err := a.LoadConfig()
	if err != nil {
		return false
	}
	u, err := cfg.FindUser(c.Name)
	return err == nil && u.Admin
}
