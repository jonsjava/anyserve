package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/jonsjava/anyserve/internal/app"
)

// UserInfo is a user as the API shows it. Keys are shown by fingerprint
// comment only, never as a login secret (they're public keys anyway).
type UserInfo struct {
	Name      string   `json:"name"`
	Admin     bool     `json:"admin"`
	Keys      []string `json:"keys"`
	Instances []string `json:"instances"`
	Certs     int      `json:"certs"`
}

// NewUser is the body of POST /v1/users.
type NewUser struct {
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	Key   string `json:"key"`
}

// UserChange is the body of PATCH /v1/users/{user}: each field set is
// applied.
type UserChange struct {
	Admin   *bool    `json:"admin,omitempty"`
	AddKey  string   `json:"add_key,omitempty"`
	Grant   []string `json:"grant,omitempty"`
	Revoke  []string `json:"revoke,omitempty"`
	DropKey *int     `json:"drop_key,omitempty"`
}

// IssuedCert is a new browser client certificate. Password is shown once.
type IssuedCert struct {
	Serial   string `json:"serial"`
	NotAfter string `json:"not_after"`
	P12      []byte `json:"p12"`
	Password string `json:"password"`
}

// admin is like begin, but only for callers with admin power.
func (s *Server) admin(h func(q *request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, ok := s.begin(w, r)
		if !ok {
			return
		}
		if !q.app.IsAdmin(q.caller) {
			fail(w, http.StatusForbidden, errors.New("this needs an admin with their browser client certificate, or the host itself"), "")
			return
		}
		h(q)
	}
}

// adminOnly refuses an instance request from a caller without admin
// power.
func (s *Server) adminOnly(h func(q *request)) func(q *request) {
	return func(q *request) {
		if !q.app.IsAdmin(q.caller) {
			fail(q.w, http.StatusForbidden, errors.New("this needs an admin with their browser client certificate, or the host itself"), "")
			return
		}
		h(q)
	}
}

func decode(q *request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(q.w, q.r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		fail(q.w, http.StatusBadRequest, fmt.Errorf("bad request body: %w", err), "")
		return false
	}
	return true
}

func keyLabel(k string) string {
	f := strings.Fields(k)
	label := f[0]
	if len(f) > 2 {
		label += " " + strings.Join(f[2:], " ")
	} else if len(f) > 1 && len(f[1]) > 12 {
		label += " …" + f[1][len(f[1])-12:]
	}
	return label
}

func (s *Server) usersList(q *request) {
	cfg, err := q.app.LoadConfig()
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	out := []UserInfo{}
	for _, u := range cfg.Users {
		ui := UserInfo{Name: u.Name, Admin: u.Admin, Keys: []string{}, Instances: append([]string{}, u.Instances...), Certs: len(u.CertSerials)}
		for _, k := range u.Keys {
			ui.Keys = append(ui.Keys, keyLabel(k))
		}
		out = append(out, ui)
	}
	writeJSON(q.w, http.StatusOK, out)
}

func (s *Server) usersCreate(q *request) {
	var n NewUser
	if !decode(q, &n) {
		return
	}
	key, err := app.NormalizeKey(n.Key)
	if err != nil {
		fail(q.w, http.StatusBadRequest, fmt.Errorf("their SSH public key (the one line from their id_ed25519.pub): %w", err), "")
		return
	}
	if err := q.app.AddUser(n.Name, n.Admin, []string{key}); err != nil {
		fail(q.w, http.StatusBadRequest, err, "")
		return
	}
	writeJSON(q.w, http.StatusCreated, Result{OK: true})
}

func (s *Server) usersChange(q *request) {
	name := q.r.PathValue("user")
	var c UserChange
	if !decode(q, &c) {
		return
	}
	if c.Admin != nil && !*c.Admin && name == q.caller.Name {
		fail(q.w, http.StatusBadRequest, errors.New("you can't take away your own admin power here; another admin or the host can"), "")
		return
	}
	var key string
	if c.AddKey != "" {
		var err error
		if key, err = app.NormalizeKey(c.AddKey); err != nil {
			fail(q.w, http.StatusBadRequest, err, "")
			return
		}
	}
	for _, in := range c.Grant {
		if !q.app.Store.Exists(in) {
			fail(q.w, http.StatusBadRequest, fmt.Errorf("no instance %q", in), "")
			return
		}
	}
	err := q.app.UpdateUser(name, func(u *app.User) error {
		if c.Admin != nil {
			u.Admin = *c.Admin
		}
		if key != "" && !slices.Contains(u.Keys, key) {
			u.Keys = append(u.Keys, key)
		}
		if c.DropKey != nil {
			i := *c.DropKey
			if i < 0 || i >= len(u.Keys) {
				return errors.New("no such key")
			}
			if len(u.Keys) == 1 {
				return errors.New("that's their only key; add another first, or remove the user")
			}
			u.Keys = slices.Delete(u.Keys, i, i+1)
		}
		for _, in := range c.Grant {
			if !slices.Contains(u.Instances, in) {
				u.Instances = append(u.Instances, in)
			}
		}
		u.Instances = slices.DeleteFunc(u.Instances, func(s string) bool { return slices.Contains(c.Revoke, s) })
		slices.Sort(u.Instances)
		return nil
	})
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, app.ErrNoUser) {
			code = http.StatusNotFound
		}
		fail(q.w, code, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}

func (s *Server) usersDelete(q *request) {
	name := q.r.PathValue("user")
	if name == q.caller.Name {
		fail(q.w, http.StatusBadRequest, errors.New("you can't remove yourself here; another admin or the host can"), "")
		return
	}
	if err := q.app.RemoveUser(name); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, app.ErrNoUser) {
			code = http.StatusNotFound
		}
		fail(q.w, code, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true})
}

// usersCert issues a browser client certificate for an admin user.
func (s *Server) usersCert(q *request) {
	if s.IssueCert == nil {
		fail(q.w, http.StatusNotImplemented, errors.New("certificates can't be issued here"), "")
		return
	}
	name := q.r.PathValue("user")
	cfg, err := q.app.LoadConfig()
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	u, err := cfg.FindUser(name)
	if err != nil {
		fail(q.w, http.StatusNotFound, err, "")
		return
	}
	if !u.Admin {
		fail(q.w, http.StatusBadRequest, fmt.Errorf("%s isn't an admin; a certificate only matters for admins", name), "")
		return
	}
	c, err := s.IssueCert(q.app, name)
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	if err := q.app.UpdateUser(name, func(u *app.User) error {
		u.CertSerials = append(u.CertSerials, c.Serial)
		return nil
	}); err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, c)
}
