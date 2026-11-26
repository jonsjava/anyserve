package app

import (
	"io/fs"
	"path/filepath"
	"sync"
	"time"

	"github.com/jonsjava/anyserve/internal/target"
)

// Stats reads an instance's resource use: uptime, CPU, memory, network
// and disk.
func (a *App) Stats(name string) (target.Stats, error) {
	in, _, t, err := a.load(name)
	if err != nil {
		return target.Stats{}, err
	}
	s, ok := t.(target.Statser)
	if !ok {
		return target.Stats{}, target.ErrNoStats
	}
	st, err := s.Stats(in)
	st.Disk = diskUsage(in.Dir)
	return st, err
}

// Folder sizes are measured in the background and kept for a minute: a big
// game server's folder can take a while to walk, and the web UI asks every
// few seconds.
var disk struct {
	sync.Mutex
	m map[string]*diskEntry
}

type diskEntry struct {
	size    int64
	at      time.Time
	walking bool
}

const diskTTL = time.Minute

func diskUsage(dir string) int64 {
	disk.Lock()
	defer disk.Unlock()
	if disk.m == nil {
		disk.m = map[string]*diskEntry{}
	}
	e := disk.m[dir]
	if e == nil {
		e = &diskEntry{}
		disk.m[dir] = e
	}
	if !e.walking && time.Since(e.at) > diskTTL {
		e.walking = true
		done := make(chan struct{})
		go func() {
			n := folderSize(dir)
			disk.Lock()
			e.size, e.at, e.walking = n, time.Now(), false
			disk.Unlock()
			close(done)
		}()
		if e.at.IsZero() {
			// The first time, wait briefly so small folders show at once.
			disk.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
			disk.Lock()
		}
	}
	return e.size
}

func folderSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}
