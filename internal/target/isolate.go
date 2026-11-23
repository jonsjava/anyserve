package target

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

// Native servers can run isolated: the supervisor stays on the host (so
// consoles, logs and clean stops work as usual), and only the server
// process runs in a container that sees nothing but the instance's server
// folder, as the installing user, with no capabilities.

// IsolationContainer is the Instance.Isolation value for containers.
const IsolationContainer = "container"

// CanIsolate reports whether a native definition can run isolated here.
func CanIsolate(d *spec.Definition) error {
	if d.IsStack() {
		return errors.New("stacks already run in containers")
	}
	if runtime.GOOS != "linux" {
		return errors.New("isolated native servers need Linux for now")
	}
	if d.Container == nil {
		return fmt.Errorf("%s's definition has no container hints (container.image) yet", d.Name)
	}
	if _, ok := d.Run["linux"]; !ok {
		return fmt.Errorf("%s has no Linux server, which a container needs", d.Name)
	}
	if err := (&docker{}).Available(); err != nil {
		return err
	}
	return nil
}

// NativeImage is the local image tag for a definition's container hints.
// The tag changes when the hints do, so a changed definition gets a fresh
// image.
func NativeImage(d *spec.Definition) string {
	pk := append([]string(nil), d.Container.Packages...)
	sort.Strings(pk)
	sum := sha256.Sum256([]byte(d.Container.Image + "\n" + strings.Join(pk, " ")))
	return "anyserve-native/" + d.ID + ":" + hex.EncodeToString(sum[:6])
}

// NativeDockerfile is the image recipe: the hinted base image plus its
// packages. Server files are never baked in; they're mounted.
func NativeDockerfile(d *spec.Definition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FROM %s\n", d.Container.Image)
	if len(d.Container.Packages) > 0 {
		pk := append([]string(nil), d.Container.Packages...)
		sort.Strings(pk)
		fmt.Fprintf(&b, "RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*\n", strings.Join(pk, " "))
	}
	fmt.Fprintf(&b, "LABEL dev.anyserve.definition=%q\n", d.ID)
	return b.String()
}

var validPackage = func(p string) bool {
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("+-.:", r)) {
			return false
		}
	}
	return p != ""
}

// EnsureNativeImage builds the definition's image unless it exists.
func EnsureNativeImage(d *spec.Definition, out io.Writer) (string, error) {
	tag := NativeImage(d)
	if exec.Command("docker", "image", "inspect", tag).Run() == nil {
		return tag, nil
	}
	for _, p := range d.Container.Packages {
		if !validPackage(p) {
			return "", fmt.Errorf("container.packages: %q isn't a package name", p)
		}
	}
	if out != nil {
		fmt.Fprintf(out, "Building the container image for %s (once per definition change)\n", d.Name)
	}
	cmd := exec.Command("docker", "build", "--quiet", "--tag", tag, "-")
	cmd.Stdin = strings.NewReader(NativeDockerfile(d))
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("building %s: %w\n%s", tag, err, strings.TrimSpace(buf.String()))
	}
	return tag, nil
}

// ContainerName is the isolated server's container name.
func ContainerName(in *instance.Instance) string { return "anyserve-" + in.Name }

// IsolatedCommand wraps a server command in `docker run`. env is the
// server's own environment (KEY=value); the host's isn't passed in.
func IsolatedCommand(in *instance.Instance, d *spec.Definition, image, workdir, command string, args, env []string) (string, []string) {
	ports, _ := d.ResolvePorts(in.Ports)
	a := []string{"run", "--rm", "--interactive", "--init",
		"--name", ContainerName(in),
		"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()),
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--pids-limit", "8192",
		"--volume", in.ServerDir + ":" + in.ServerDir,
		"--workdir", workdir,
		"--env", "HOME=" + in.ServerDir,
		"--label", "dev.anyserve.instance=" + in.Name,
	}
	for _, e := range env {
		a = append(a, "--env", e)
	}
	for _, p := range d.Ports {
		host := "127.0.0.1:"
		if p.Public || in.Exposed(p.Name) {
			host = ""
		}
		n := strconv.Itoa(ports[p.Name])
		for _, proto := range protosOf(p.Protocol) {
			a = append(a, "--publish", host+n+":"+n+"/"+proto)
		}
	}
	a = append(a, image, command)
	return "docker", append(a, args...)
}

func protosOf(p string) []string {
	switch p {
	case "udp":
		return []string{"udp"}
	case "both":
		return []string{"tcp", "udp"}
	}
	return []string{"tcp"}
}

// RemoveIsolated removes a leftover isolated container (after a crash, or
// when the supervisor had to kill the docker client).
func RemoveIsolated(in *instance.Instance) {
	exec.Command("docker", "rm", "--force", ContainerName(in)).Run()
}
