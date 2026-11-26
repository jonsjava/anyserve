package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jonsjava/anyserve/internal/app"
)

func TestReservationsAPI(t *testing.T) {
	s, _ := testServer(t)
	alice := app.Caller{Name: "alice", Cert: true}
	for _, m := range []string{"GET", "POST", "DELETE"} {
		if code, _ := call(t, s, &alice, m, "/v1/reservations", strings.NewReader(`{"from":41000}`)); code != http.StatusForbidden {
			t.Errorf("a user could %s reservations: %d", m, code)
		}
	}
	owner := app.Owner
	if code, body := call(t, s, &owner, "GET", "/v1/reservations", nil); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Errorf("empty list: %d %s", code, body)
	}
	if code, body := call(t, s, &owner, "POST", "/v1/reservations", strings.NewReader(`{"from":41000,"to":41009,"note":"panel"}`)); code != http.StatusCreated {
		t.Fatalf("add: %d %s", code, body)
	}
	if code, _ := call(t, s, &owner, "POST", "/v1/reservations", strings.NewReader(`{"from":41020,"protocol":"udp"}`)); code != http.StatusCreated {
		t.Fatalf("add one port: %d", code)
	}
	if code, _ := call(t, s, &owner, "POST", "/v1/reservations", strings.NewReader(`{"from":9,"to":1}`)); code != http.StatusBadRequest {
		t.Errorf("a backwards range: %d", code)
	}
	_, body := call(t, s, &owner, "GET", "/v1/reservations", nil)
	if !strings.Contains(body, `"note": "panel"`) || !strings.Contains(body, `"to": 41020`) || !strings.Contains(body, `"protocol": "both"`) {
		t.Errorf("list: %s", body)
	}
	if code, _ := call(t, s, &owner, "DELETE", "/v1/reservations", strings.NewReader(`{"from":41000,"to":41009}`)); code != 200 {
		t.Errorf("remove: %d", code)
	}
	if code, _ := call(t, s, &owner, "DELETE", "/v1/reservations", strings.NewReader(`{"from":41000,"to":41009}`)); code != http.StatusNotFound {
		t.Errorf("removing twice: %d", code)
	}
}
