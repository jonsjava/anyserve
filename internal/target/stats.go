package target

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonsjava/anyserve/internal/instance"
)

// Stats is one reading of an instance's resource use. CPU time and network
// bytes are running totals, so callers turn two readings into rates.
type Stats struct {
	At      time.Time `json:"at"`
	Running bool      `json:"running"`
	// Started is when the server last started (its uptime).
	Started time.Time `json:"started,omitzero"`
	// CPU is total CPU time used, in nanoseconds; Cores is how many the
	// machine has (100% is one whole core, as in most panels).
	CPU   uint64 `json:"cpu_ns"`
	Cores int    `json:"cores"`
	// Memory is in use now; MemoryMax is its limit, or the machine's RAM.
	Memory    uint64 `json:"memory"`
	MemoryMax uint64 `json:"memory_max"`
	// NetRx and NetTx are bytes received and sent, when Net is true.
	// Native servers share the machine's network, so theirs can't be told
	// apart unless they're isolated in a container.
	Net   bool   `json:"net"`
	NetRx uint64 `json:"net_rx"`
	NetTx uint64 `json:"net_tx"`
	// Processes is set for native servers (a container counts threads).
	Processes int `json:"processes,omitempty"`
	// Disk is the size of the instance's folder (filled in by the app).
	Disk int64 `json:"disk"`
}

// Statser is a target that can report resource use.
type Statser interface {
	Stats(in *instance.Instance) (Stats, error)
}

func newStats() Stats {
	return Stats{At: time.Now().UTC(), Cores: runtime.NumCPU(), MemoryMax: hostMemory()}
}

// ---- systemd: the unit's cgroup ----

func (s *systemd) Stats(in *instance.Instance) (Stats, error) {
	st := newStats()
	if in.Isolation == "container" {
		// The server runs in Docker's container, not the unit's cgroup.
		return containerStats(st, []string{ContainerName(in)})
	}
	// Unix-time timestamps: the monotonic one skips time spent suspended.
	out, err := exec.Command("systemctl", "--user", "show", "--timestamp=unix", unitName(in), "-p", "ActiveState,ControlGroup,ActiveEnterTimestamp").Output()
	if err != nil {
		return st, fmt.Errorf("systemctl --user show: %w", err)
	}
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			props[k] = v
		}
	}
	if props["ActiveState"] != "active" && props["ActiveState"] != "reloading" {
		return st, nil
	}
	st.Running = true
	if sec, err := strconv.ParseInt(strings.TrimPrefix(props["ActiveEnterTimestamp"], "@"), 10, 64); err == nil && sec > 0 {
		st.Started = time.Unix(sec, 0).UTC()
	}
	cg := props["ControlGroup"]
	if cg == "" {
		return st, nil
	}
	dir := filepath.Join("/sys/fs/cgroup", cg)
	if v, ok := cgroupKey(filepath.Join(dir, "cpu.stat"), "usage_usec"); ok {
		st.CPU = v * 1000
	}
	pids := cgroupPids(dir)
	st.Processes = len(pids)
	if v, ok := readUint(filepath.Join(dir, "memory.current")); ok {
		// Like `docker stats`, leave out page cache the kernel can drop.
		if inactive, ok := cgroupKey(filepath.Join(dir, "memory.stat"), "inactive_file"); ok && inactive < v {
			v -= inactive
		}
		st.Memory = v
	} else {
		// No memory controller for user services here: add up the
		// processes' resident memory instead.
		for _, p := range pids {
			st.Memory += rss(p)
		}
	}
	if v, ok := readUint(filepath.Join(dir, "memory.max")); ok && v < st.MemoryMax {
		st.MemoryMax = v
	}
	return st, nil
}

// ---- docker: the compose project's containers ----

func (t *docker) Stats(in *instance.Instance) (Stats, error) {
	st := newStats()
	c, err := dockerAPI()
	if err != nil {
		return st, err
	}
	filter, _ := json.Marshal(map[string][]string{"label": {"com.docker.compose.project=" + project(in)}})
	var list []struct {
		ID    string `json:"Id"`
		State string `json:"State"`
	}
	if err := c.get("/containers/json?filters="+url.QueryEscape(string(filter)), &list); err != nil {
		return st, err
	}
	var ids []string
	for _, l := range list {
		if l.State == "running" {
			ids = append(ids, l.ID)
		}
	}
	return containerStats(st, ids)
}

// containerStats adds up running containers' use. Uptime is the oldest
// container's, which for a stack is when it was last started.
func containerStats(st Stats, ids []string) (Stats, error) {
	if len(ids) == 0 {
		return st, nil
	}
	c, err := dockerAPI()
	if err != nil {
		return st, err
	}
	var limit uint64
	for _, id := range ids {
		var info struct {
			State struct {
				Running   bool      `json:"Running"`
				StartedAt time.Time `json:"StartedAt"`
			} `json:"State"`
		}
		if err := c.get("/containers/"+url.PathEscape(id)+"/json", &info); err != nil || !info.State.Running {
			continue // gone or stopped since
		}
		var s struct {
			CPU struct {
				Usage struct {
					Total uint64 `json:"total_usage"`
				} `json:"cpu_usage"`
			} `json:"cpu_stats"`
			Memory struct {
				Usage uint64            `json:"usage"`
				Limit uint64            `json:"limit"`
				Stats map[string]uint64 `json:"stats"`
			} `json:"memory_stats"`
			Networks map[string]struct {
				Rx uint64 `json:"rx_bytes"`
				Tx uint64 `json:"tx_bytes"`
			} `json:"networks"`
		}
		if err := c.get("/containers/"+url.PathEscape(id)+"/stats?stream=false&one-shot=true", &s); err != nil {
			continue
		}
		st.Running = true
		if st.Started.IsZero() || info.State.StartedAt.Before(st.Started) {
			st.Started = info.State.StartedAt.UTC()
		}
		st.CPU += s.CPU.Usage.Total
		mem := s.Memory.Usage
		if cache := s.Memory.Stats["inactive_file"]; cache < mem {
			mem -= cache
		}
		st.Memory += mem
		if s.Memory.Limit > 0 && s.Memory.Limit < st.MemoryMax {
			limit += s.Memory.Limit
		}
		if s.Networks != nil {
			st.Net = true
		}
		for _, n := range s.Networks {
			st.NetRx += n.Rx
			st.NetTx += n.Tx
		}
	}
	if limit > 0 && limit < st.MemoryMax {
		st.MemoryMax = limit
	}
	return st, nil
}

// dockerClient talks to the Docker Engine API on its unix socket.
type dockerClient struct{ http *http.Client }

var (
	dockerOnce sync.Once
	dockerC    *dockerClient
	dockerErr  error
)

func dockerAPI() (*dockerClient, error) {
	dockerOnce.Do(func() {
		sock, err := dockerSocket()
		if err != nil {
			dockerErr = err
			return
		}
		dockerC = &dockerClient{http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			}},
		}}
	})
	return dockerC, dockerErr
}

// dockerSocket finds the socket the docker command uses (DOCKER_HOST, the
// current context, rootless Docker), as long as it's a local unix socket.
func dockerSocket() (string, error) {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		out, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		if err == nil {
			host = strings.TrimSpace(string(out))
		}
	}
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	sock, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		return "", fmt.Errorf("stats need a local Docker socket (Docker is at %s)", host)
	}
	return sock, nil
}

func (c *dockerClient) get(path string, v any) error {
	resp, err := c.http.Get("http://docker" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// ---- /proc and cgroup helpers (Linux) ----

func readUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

// cgroupKey reads one "key value" line from a cgroup stat file.
func cgroupKey(path, key string) (uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), " "); ok && k == key {
			n, err := strconv.ParseUint(v, 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// cgroupPids lists the processes in a cgroup and its children.
func cgroupPids(dir string) []int {
	var pids []int
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "cgroup.procs" {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				pids = append(pids, n)
			}
		}
		return nil
	})
	return pids
}

var pageSize = uint64(os.Getpagesize())

func rss(pid int) uint64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.ParseUint(f[1], 10, 64)
	return n * pageSize
}

func hostMemory() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) >= 2 && fs[0] == "MemTotal:" {
			n, _ := strconv.ParseUint(fs[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}

// ErrNoStats is returned for targets that can't report resource use.
var ErrNoStats = errors.New("this kind of server doesn't report resource use yet")
