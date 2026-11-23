package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/app"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIItuV09ej9HRGq8uLuIBqrZsqaDcuLjtBpxTkWtP5TtA bob@laptop"

func TestUserAdmin(t *testing.T) {
	s, _ := testServer(t)
	s.IssueCert = func(a *app.App, user string) (*IssuedCert, error) {
		return &IssuedCert{Serial: "abc123", P12: []byte("p12"), Password: "pw"}, nil
	}
	a, _ := s.NewApp(nil)
	if err := a.AddUser("sam", true, nil); err != nil {
		t.Fatal(err)
	}
	alice := app.Caller{Name: "alice", Cert: true}
	samNoCert := app.Caller{Name: "sam", Admin: true}
	sam := app.Caller{Name: "sam", Admin: true, Cert: true}

	for _, c := range []app.Caller{alice, samNoCert} {
		if code, _ := call(t, s, &c, "GET", "/v1/users", nil); code != http.StatusForbidden {
			t.Errorf("%+v listed users: %d", c, code)
		}
		if code, _ := call(t, s, &c, "POST", "/v1/instances", strings.NewReader(`{"service":"gotify","name":"x"}`)); code != http.StatusForbidden {
			t.Errorf("%+v could install: %d", c, code)
		}
		// alice manages alpha but isn't an admin (403); sam without a
		// certificate can't see it at all (404).
		if code, _ := call(t, s, &c, "DELETE", "/v1/instances/alpha", strings.NewReader(`{"confirm":"alpha"}`)); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Errorf("%+v could remove: %d", c, code)
		}
	}

	code, body := call(t, s, &sam, "POST", "/v1/users", strings.NewReader(`{"name":"bob","key":"`+testKey+`"}`))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	if code, _ := call(t, s, &sam, "POST", "/v1/users", strings.NewReader(`{"name":"carol","key":"not a key"}`)); code != http.StatusBadRequest {
		t.Errorf("bad key accepted: %d", code)
	}
	code, body = call(t, s, &sam, "PATCH", "/v1/users/bob", strings.NewReader(`{"grant":["beta"],"admin":true}`))
	if code != http.StatusOK {
		t.Fatalf("change: %d %s", code, body)
	}
	if code, _ := call(t, s, &sam, "PATCH", "/v1/users/bob", strings.NewReader(`{"grant":["nope"]}`)); code != http.StatusBadRequest {
		t.Errorf("grant of a missing instance: %d", code)
	}
	if code, _ := call(t, s, &sam, "PATCH", "/v1/users/sam", strings.NewReader(`{"admin":false}`)); code != http.StatusBadRequest {
		t.Errorf("self-demotion allowed: %d", code)
	}
	if code, _ := call(t, s, &sam, "PATCH", "/v1/users/bob", strings.NewReader(`{"drop_key":0}`)); code != http.StatusBadRequest {
		t.Errorf("dropped the only key: %d", code)
	}
	_, body = call(t, s, &sam, "GET", "/v1/users", nil)
	var users []UserInfo
	json.Unmarshal([]byte(body), &users)
	var bob *UserInfo
	for i := range users {
		if users[i].Name == "bob" {
			bob = &users[i]
		}
	}
	if bob == nil || !bob.Admin || len(bob.Instances) != 1 || bob.Keys[0] != "ssh-ed25519 bob@laptop" {
		t.Fatalf("bob: %+v", bob)
	}
	code, body = call(t, s, &sam, "POST", "/v1/users/bob/cert", nil)
	if code != http.StatusOK || !strings.Contains(body, `"password": "pw"`) {
		t.Fatalf("cert: %d %s", code, body)
	}
	if code, _ := call(t, s, &sam, "POST", "/v1/users/alice/cert", nil); code != http.StatusBadRequest {
		t.Errorf("cert for a non-admin: %d", code)
	}
	if code, _ := call(t, s, &sam, "DELETE", "/v1/users/sam", nil); code != http.StatusBadRequest {
		t.Errorf("self-removal allowed: %d", code)
	}
	if code, _ := call(t, s, &sam, "DELETE", "/v1/users/bob", nil); code != http.StatusOK {
		t.Errorf("remove bob: %d", code)
	}
	// The owner can do it all too.
	if code, _ := call(t, s, &app.Owner, "GET", "/v1/users", nil); code != http.StatusOK {
		t.Errorf("owner: %d", code)
	}
	// Removal needs the name typed back.
	if code, _ := call(t, s, &sam, "DELETE", "/v1/instances/alpha", strings.NewReader(`{"confirm":"beta"}`)); code != http.StatusBadRequest {
		t.Errorf("unconfirmed removal: %d", code)
	}
	// A dry run plans without installing.
	code, body = call(t, s, &sam, "POST", "/v1/instances", strings.NewReader(`{"service":"gotify","name":"push","dry_run":true}`))
	if code != http.StatusOK || !strings.Contains(body, `"ports"`) {
		t.Errorf("dry run: %d %s", code, body)
	}
	if a.Store.Exists("push") {
		t.Error("a dry run installed something")
	}
}
