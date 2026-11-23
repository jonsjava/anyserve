// Package https puts apps behind an HTTPS reverse proxy with real
// certificates. Caddy is the default and Anyserve runs it; people who
// already run nginx or Traefik get a config file for their own proxy
// instead; "none" turns it off.
package https

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Providers.
const (
	Caddy   = "caddy"
	Nginx   = "nginx"
	Traefik = "traefik"
	None    = "none"
)

// Providers lists the choices, default first.
var Providers = []string{Caddy, Nginx, Traefik, None}

// CaddyImage is the pinned Caddy image Anyserve runs.
const CaddyImage = "caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d"

// Site is one domain proxied to an app's local port.
type Site struct {
	Domain   string
	Port     int
	Instance string
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidDomain checks a domain name such as vault.example.com.
func ValidDomain(d string) error {
	if !domainRe.MatchString(d) || len(d) > 253 {
		return fmt.Errorf("%q isn't a domain name like vault.example.com", d)
	}
	return nil
}

func sorted(sites []Site) []Site {
	out := append([]Site(nil), sites...)
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}

// Caddyfile renders the managed Caddy's config. The admin API is on a
// unix socket in Caddy's config folder, so it doesn't take a port.
//
// passthrough lists servers that do their own TLS: connections to their
// domains on 443 are passed through untouched, chosen by the name in the
// TLS handshake (SNI), which needs Caddy built with caddy-l4.
func Caddyfile(email string, sites, passthrough []Site) string {
	var b strings.Builder
	b.WriteString("# Managed by Anyserve (`anyserve https`). Changes are overwritten.\n{\n\tadmin unix//config/admin.sock\n")
	if email != "" {
		fmt.Fprintf(&b, "\temail %s\n", email)
	}
	if len(passthrough) > 0 && len(sites) == 0 {
		// The placeholder site below uses Caddy's own CA; don't try to
		// add it to the system's trust store.
		b.WriteString("\tskip_install_trust\n")
	}
	if len(passthrough) > 0 {
		b.WriteString("\tservers :443 {\n\t\tlistener_wrappers {\n\t\t\tlayer4 {\n")
		for i, s := range sorted(passthrough) {
			fmt.Fprintf(&b, "\t\t\t\t# %s\n\t\t\t\t@p%d tls sni %s\n\t\t\t\troute @p%d {\n\t\t\t\t\tproxy 127.0.0.1:%d\n\t\t\t\t}\n", s.Instance, i, s.Domain, i, s.Port)
		}
		b.WriteString("\t\t\t}\n\t\t\ttls\n\t\t}\n\t}\n")
	}
	b.WriteString("}\n")
	for _, s := range sorted(sites) {
		fmt.Fprintf(&b, "\n# %s\n%s {\n\tencode zstd gzip\n\treverse_proxy 127.0.0.1:%d\n}\n", s.Instance, s.Domain, s.Port)
	}
	if len(passthrough) > 0 && len(sites) == 0 {
		// Caddy only listens on 443 for a site there; this one answers
		// anything the passthrough routes don't match.
		b.WriteString("\n# Keeps port 443 open for the servers above.\n:443 {\n\ttls internal\n\trespond 404\n}\n")
	}
	return b.String()
}

// CaddyL4Dockerfile builds Caddy with the caddy-l4 plugin, for passing TLS
// through by name. Anyserve builds it on this machine only when a server
// needs it, from source with a pinned Go image, xcaddy, Caddy and plugin
// (both Apache-2.0), onto a pinned Alpine; no prebuilt Caddy image is used.
const CaddyL4Dockerfile = `FROM golang:1.26.9-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@v0.4.7
RUN xcaddy build v2.11.7 --with github.com/mholt/caddy-l4@v0.1.2 --output /caddy
FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates libcap mailcap
COPY --from=build /caddy /usr/bin/caddy
RUN setcap cap_net_bind_service=+ep /usr/bin/caddy
`

// CaddyL4Image is the local tag for CaddyL4Dockerfile's image; the tag
// changes whenever the Dockerfile does.
func CaddyL4Image() string {
	sum := sha256.Sum256([]byte(CaddyL4Dockerfile))
	return "anyserve-caddy-l4:" + hex.EncodeToString(sum[:6])
}

// HaveCaddyL4 reports whether the passthrough Caddy is already built.
func HaveCaddyL4() bool {
	return exec.Command("docker", "image", "inspect", CaddyL4Image()).Run() == nil
}

func ensureCaddyL4() error {
	img := CaddyL4Image()
	if HaveCaddyL4() {
		return nil
	}
	cmd := exec.Command("docker", "build", "-t", img, "-")
	cmd.Stdin = strings.NewReader(CaddyL4Dockerfile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building Caddy with TLS passthrough: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// NginxConf renders server blocks for an nginx that the user runs. TLS
// certificates are theirs to provide (certbot writes these paths).
func NginxConf(sites []Site) string {
	var b strings.Builder
	b.WriteString("# Written by Anyserve (`anyserve https sync`). Include it from nginx.conf's http block.\n# Certificates: certbot certonly --nginx -d <domain> (or your own).\n")
	for _, s := range sorted(sites) {
		fmt.Fprintf(&b, `
# %[3]s
server {
    listen 80;
    listen [::]:80;
    server_name %[1]s;
    return 301 https://$host$request_uri;
}
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name %[1]s;
    ssl_certificate     /etc/letsencrypt/live/%[1]s/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/%[1]s/privkey.pem;
    client_max_body_size 0;
    location / {
        proxy_pass http://127.0.0.1:%[2]d;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 1h;
    }
}
`, s.Domain, s.Port, s.Instance)
	}
	if len(sites) > 0 {
		b.WriteString("\n# Needed once in the http block for WebSockets:\n# map $http_upgrade $connection_upgrade { default upgrade; '' close; }\n")
	}
	return b.String()
}

// TraefikConf renders a dynamic configuration file for Traefik's file
// provider. It assumes an entry point named websecure and a certificate
// resolver named letsencrypt; rename them to match your Traefik.
func TraefikConf(sites []Site) string {
	var b strings.Builder
	b.WriteString("# Written by Anyserve (`anyserve https sync`) for Traefik's file provider.\n# Assumes entry point \"websecure\" and certificate resolver \"letsencrypt\".\n")
	if len(sites) == 0 {
		b.WriteString("http: {}\n")
		return b.String()
	}
	b.WriteString("http:\n  routers:\n")
	for _, s := range sorted(sites) {
		id := "anyserve-" + s.Instance
		fmt.Fprintf(&b, "    %s:\n      rule: \"Host(`%s`)\"\n      entryPoints: [websecure]\n      service: %s\n      tls:\n        certResolver: letsencrypt\n", id, s.Domain, id)
	}
	b.WriteString("  services:\n")
	for _, s := range sorted(sites) {
		fmt.Fprintf(&b, "    anyserve-%s:\n      loadBalancer:\n        servers:\n          - url: \"http://127.0.0.1:%d\"\n", s.Instance, s.Port)
	}
	return b.String()
}

// CaddyCompose is the managed Caddy's compose file. Caddy needs the host's
// network to reach apps on 127.0.0.1 and to take ports 80 and 443. It runs
// as the user with every capability dropped except binding low ports
// (which the image's caddy binary carries as a file capability, so
// no-new-privileges can't be set for this one container).
func CaddyCompose(uid, gid int, image string) string {
	return fmt.Sprintf(`# Managed by Anyserve (`+"`anyserve https`"+`).
name: anyserve-https
services:
  caddy:
    image: %s
    network_mode: host
    user: "%d:%d"
    cap_drop: [ALL]
    cap_add: [NET_BIND_SERVICE]
    restart: unless-stopped
    read_only: true
    command: ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
    environment:
      XDG_DATA_HOME: /data
      XDG_CONFIG_HOME: /config
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - ./data:/data
      - ./config:/config
    tmpfs:
      - /tmp
`, image, uid, gid)
}

// Managed runs Anyserve's own Caddy from a folder.
type Managed struct{ Dir string }

func (m Managed) compose(args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "-f", filepath.Join(m.Dir, "compose.yaml")}, args...)...)
	cmd.Dir = m.Dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w\n%s", args[0], err, strings.TrimSpace(buf.String()))
	}
	return nil
}

// Apply writes the config and starts or reloads Caddy; with no sites it
// stops Caddy (keeping its certificates).
func (m Managed) Apply(email string, sites, passthrough []Site) error {
	for _, d := range []string{m.Dir, filepath.Join(m.Dir, "data"), filepath.Join(m.Dir, "config")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if len(sites) == 0 && len(passthrough) == 0 {
		if err := os.WriteFile(filepath.Join(m.Dir, "Caddyfile"), []byte(Caddyfile(email, nil, nil)), 0o644); err != nil {
			return err
		}
		return m.Stop()
	}
	image := CaddyImage
	if len(passthrough) > 0 {
		if err := ensureCaddyL4(); err != nil {
			return err
		}
		image = CaddyL4Image()
	}
	compose := filepath.Join(m.Dir, "compose.yaml")
	want := CaddyCompose(os.Getuid(), os.Getgid(), image)
	old, _ := os.ReadFile(compose)
	if err := os.WriteFile(compose, []byte(want), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.Dir, "Caddyfile"), []byte(Caddyfile(email, sites, passthrough)), 0o644); err != nil {
		return err
	}
	if string(old) == want && m.Running() {
		// Reload in place: no dropped connections, and a bad config is
		// refused while the old one keeps serving.
		cmd := exec.Command("docker", "compose", "-f", filepath.Join(m.Dir, "compose.yaml"), "exec", "-T", "caddy",
			"caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile", "--address", "unix//config/admin.sock")
		cmd.Dir = m.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("reloading Caddy: %w\n%s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return m.compose("up", "--detach", "--remove-orphans")
}

// Running reports whether the managed Caddy is up.
func (m Managed) Running() bool {
	if _, err := os.Stat(filepath.Join(m.Dir, "compose.yaml")); err != nil {
		return false
	}
	cmd := exec.Command("docker", "compose", "-f", filepath.Join(m.Dir, "compose.yaml"), "ps", "--status", "running", "--quiet")
	cmd.Dir = m.Dir
	out, err := cmd.Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

// Stop stops the managed Caddy, if it was set up.
func (m Managed) Stop() error {
	if _, err := os.Stat(filepath.Join(m.Dir, "compose.yaml")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return m.compose("down")
}

// PortFree reports whether a TCP port on all addresses is free to take.
func PortFree(port int) bool {
	cmd := exec.Command("sh", "-c", "ss -Hltn 'sport = :"+strconv.Itoa(port)+"'")
	out, err := cmd.Output()
	return err == nil && len(bytes.TrimSpace(out)) == 0
}
