package app

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
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
}

// RemoteConfig holds the built-in SSH server's settings.
type RemoteConfig struct {
	// Listen is the address the SSH server listens on, e.g. 0.0.0.0:7022.
	Listen string `json:"listen,omitempty"`
}

// DefaultRemoteListen is where the SSH server listens unless told
// otherwise: this machine only, so opening it to the network is a choice.
const DefaultRemoteListen = "127.0.0.1:7022"

// Caller is who's making an API request.
type Caller struct {
	Name  string
	Admin bool
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
	if c.Admin {
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
	// Re-check admin from the config, so a demotion applies at once.
	return u.Admin || slices.Contains(u.Instances, instance)
}

// CallerFor returns the caller for a configured user, with their current
// admin flag.
func (a *App) CallerFor(name string) (Caller, error) {
	cfg, err := a.LoadConfig()
	if err != nil {
		return Caller{}, err
	}
	u, err := cfg.FindUser(name)
	if err != nil {
		return Caller{}, err
	}
	return Caller{Name: u.Name, Admin: u.Admin}, nil
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

// NormalizeKey checks a public key line and drops surrounding space.
func NormalizeKey(line string) (string, error) {
	line = strings.TrimSpace(line)
	f := strings.Fields(line)
	if len(f) < 2 || !strings.HasPrefix(f[0], "ssh-") && !strings.HasPrefix(f[0], "ecdsa-") && !strings.HasPrefix(f[0], "sk-") {
		return "", errors.New("not an SSH public key (expected a line like \"ssh-ed25519 AAAA... you@laptop\")")
	}
	return line, nil
}
