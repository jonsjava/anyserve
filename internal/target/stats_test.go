package target

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCgroupFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 1500\nuser_usec 1000\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "memory.current"), []byte("4096\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "child"), 0o755)
	os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("10\n11\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "child", "cgroup.procs"), []byte("12\n"), 0o644)
	if v, ok := cgroupKey(filepath.Join(dir, "cpu.stat"), "usage_usec"); !ok || v != 1500 {
		t.Errorf("usage_usec: %d %v", v, ok)
	}
	if _, ok := cgroupKey(filepath.Join(dir, "cpu.stat"), "nope"); ok {
		t.Error("found a missing key")
	}
	if v, ok := readUint(filepath.Join(dir, "memory.current")); !ok || v != 4096 {
		t.Errorf("memory.current: %d %v", v, ok)
	}
	if _, ok := readUint(filepath.Join(dir, "missing")); ok {
		t.Error("read a missing file")
	}
	if p := cgroupPids(dir); len(p) != 3 {
		t.Errorf("pids: %v", p)
	}
}

// Containers' use adds up; the oldest start is the uptime; stopped
// containers are skipped.
func TestContainerStats(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets:", err)
	}
	mux := http.NewServeMux()
	inspect := map[string]string{
		"a": `{"State":{"Running":true,"StartedAt":"2026-01-02T03:04:05Z"}}`,
		"b": `{"State":{"Running":true,"StartedAt":"2026-01-01T00:00:00Z"}}`,
		"c": `{"State":{"Running":false}}`,
	}
	stats := `{"cpu_stats":{"cpu_usage":{"total_usage":2000000000}},"memory_stats":{"usage":300,"limit":1000,"stats":{"inactive_file":100}},"networks":{"eth0":{"rx_bytes":10,"tx_bytes":20},"eth1":{"rx_bytes":1,"tx_bytes":2}}}`
	mux.HandleFunc("/containers/{id}/json", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(inspect[r.PathValue("id")])) })
	mux.HandleFunc("/containers/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "false" {
			t.Error("stats must not stream")
		}
		w.Write([]byte(stats))
	})
	go http.Serve(l, mux)
	defer l.Close()
	dockerOnce.Do(func() {}) // don't look for the real socket
	old := dockerC
	dockerC = &dockerClient{http: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Dial: func(_, _ string) (net.Conn, error) { return net.Dial("unix", sock) }}}}
	defer func() { dockerC = old }()

	st, err := containerStats(Stats{MemoryMax: 1 << 40}, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.CPU != 4e9 || st.Memory != 400 || st.MemoryMax != 2000 {
		t.Errorf("totals: %+v", st)
	}
	if !st.Net || st.NetRx != 22 || st.NetTx != 44 {
		t.Errorf("network: %+v", st)
	}
	if !strings.HasPrefix(st.Started.Format(time.RFC3339), "2026-01-01T00:00:00") {
		t.Errorf("uptime should be the oldest container's: %v", st.Started)
	}
	if st, _ := containerStats(Stats{}, nil); st.Running {
		t.Error("no containers counted as running")
	}
}
