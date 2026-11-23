package https

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RouterImage is the Minecraft hostname router: it reads the server address
// a player typed from the handshake and passes the connection to that
// server, so many Minecraft servers share one public port.
const RouterImage = "itzg/mc-router:1.55.0@sha256:0e89cb4e8e1e73f9ccec4211fab5c6b8336bd2768b82b7cca25c6290ed5a2f4a"

// DefaultRouterPort is the standard Minecraft port, which players don't
// have to type.
const DefaultRouterPort = 25565

// RouterCompose is the router's Compose file. It runs on the host network
// as the user, read-only and with no capabilities.
func RouterCompose(uid, gid, port int) string {
	return fmt.Sprintf(`# Managed by Anyserve (Minecraft servers with domains).
name: anyserve-router
services:
  mc-router:
    image: %s
    network_mode: host
    user: "%d:%d"
    cap_drop: [ALL]
    security_opt: [no-new-privileges]
    restart: unless-stopped
    read_only: true
    command: ["-port", "%d", "-routes-config", "/config/routes.json", "-routes-config-watch", "-connection-rate-limit", "20"]
    volumes:
      - ./config:/config:ro
`, RouterImage, uid, gid, port)
}

// RouterRoutes is the router's routes file: domain to 127.0.0.1:port.
func RouterRoutes(routes map[string]int) ([]byte, error) {
	m := map[string]string{}
	keys := make([]string, 0, len(routes))
	for d := range routes {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	for _, d := range keys {
		m[d] = fmt.Sprintf("127.0.0.1:%d", routes[d])
	}
	b, err := json.MarshalIndent(map[string]any{"mappings": m}, "", "  ")
	return append(b, '\n'), err
}

// Router manages the Minecraft router's container in Dir.
type Router struct {
	Dir string
	// Restarted is set when Apply had to restart the router.
	Restarted bool
}

// Apply writes the routes and starts, updates or stops the router. The
// router watches its routes file, so changing routes doesn't drop players.
func (r *Router) Apply(port int, routes map[string]int) error {
	m := Managed{Dir: r.Dir}
	if len(routes) == 0 {
		return m.Stop()
	}
	cfg := filepath.Join(r.Dir, "config")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		return err
	}
	// The container runs as the user, so it can read a 0700 folder.
	body, err := RouterRoutes(routes)
	if err != nil {
		return err
	}
	file := filepath.Join(cfg, "routes.json")
	var before struct {
		Mappings map[string]string `json:"mappings"`
	}
	if b, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(b, &before)
	}
	// Rewrite in place: the router watches the file itself, and replacing
	// it with a rename would leave the watch on the old file.
	if err := os.WriteFile(file, body, 0o600); err != nil {
		return err
	}
	compose := filepath.Join(r.Dir, "compose.yaml")
	want := RouterCompose(os.Getuid(), os.Getgid(), port)
	old, _ := os.ReadFile(compose)
	if string(old) == want && m.Running() {
		// The router picks up new routes from the file within seconds, but
		// it never forgets one, so a removed or moved route needs a
		// restart (players on routed servers reconnect).
		if dropped(before.Mappings, routes) {
			r.Restarted = true
			return m.compose("restart", "--timeout", "2")
		}
		return nil
	}
	if err := os.WriteFile(compose, []byte(want), 0o600); err != nil {
		return err
	}
	return m.compose("up", "--detach", "--remove-orphans")
}

// dropped reports whether any old route is gone or points elsewhere now.
func dropped(before map[string]string, after map[string]int) bool {
	for d, backend := range before {
		p, ok := after[d]
		if !ok || backend != fmt.Sprintf("127.0.0.1:%d", p) {
			return true
		}
	}
	return false
}

// Running reports whether the router is up.
func (r Router) Running() bool { return Managed{Dir: r.Dir}.Running() }
