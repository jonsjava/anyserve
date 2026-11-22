package fetch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// steamcmdBootstrap lists Valve's official SteamCMD downloads. Valve doesn't
// publish checksums for these, so they're trusted on HTTPS alone, and
// SteamCMD updates itself on first run anyway.
var steamcmdBootstrap = map[string]struct{ url, exe string }{
	"linux":   {"https://steamcdn-a.akamaihd.net/client/installer/steamcmd_linux.tar.gz", "steamcmd.sh"},
	"darwin":  {"https://steamcdn-a.akamaihd.net/client/installer/steamcmd_osx.tar.gz", "steamcmd.sh"},
	"windows": {"https://steamcdn-a.akamaihd.net/client/installer/steamcmd.zip", "steamcmd.exe"},
}

// SteamCMD manages a private copy of SteamCMD.
type SteamCMD struct {
	Dir   string
	Cache string
	Log   Progress
	// Output receives SteamCMD's own output. Nil discards it.
	Output io.Writer
}

// Ensure installs SteamCMD if needed and returns its executable path.
func (s SteamCMD) Ensure(ctx context.Context) (string, error) {
	b, ok := steamcmdBootstrap[runtime.GOOS]
	if !ok {
		return "", fmt.Errorf("SteamCMD isn't available for %s", runtime.GOOS)
	}
	exe := filepath.Join(s.Dir, b.exe)
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	s.Log("Installing SteamCMD into %s", s.Dir)
	archive, err := Download(ctx, b.url, "", s.Cache, s.Log)
	if err != nil {
		return "", fmt.Errorf("install SteamCMD: %w", err)
	}
	if _, err := Extract(archive, "", s.Dir); err != nil {
		return "", fmt.Errorf("install SteamCMD: %w", err)
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/lib32/libgcc_s.so.1"); err != nil {
			if _, err2 := os.Stat("/usr/lib32/libgcc_s.so.1"); err2 != nil {
				s.Log("Warning: SteamCMD needs 32-bit libraries. On Debian or Ubuntu: sudo apt install lib32gcc-s1")
			}
		}
	}
	return exe, nil
}

// App describes one SteamCMD app install.
type App struct {
	ID  int
	Dir string
	// Platform optionally forces another OS's build (linux, windows, macos).
	Platform string
	// Beta is an optional beta branch.
	Beta string
	// User logs in with a cached Steam session instead of anonymously.
	User string
}

// AppUpdate installs or updates a Steam app.
func (s SteamCMD) AppUpdate(ctx context.Context, app App) error {
	appID, dir, platform := app.ID, app.Dir, app.Platform
	exe, err := s.Ensure(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	args := []string{}
	if platform != "" {
		args = append(args, "+@sSteamCmdForcePlatformType", platform)
	}
	// force_install_dir must come before login in current SteamCMD.
	user := app.User
	if user == "" {
		user = "anonymous"
	}
	args = append(args, "+force_install_dir", dir, "+login", user, "+app_update", strconv.Itoa(appID))
	if app.Beta != "" {
		args = append(args, "-beta", app.Beta)
	}
	args = append(args, "validate", "+quit")
	s.Log("Running SteamCMD for app %d (this can take a while)", appID)

	// SteamCMD sometimes exits non-zero on its first run while it updates
	// itself, so retry once.
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		lastErr = s.run(ctx, exe, args)
		if lastErr == nil {
			return nil
		}
		if attempt == 1 {
			s.Log("SteamCMD failed (%v); retrying once", lastErr)
		}
	}
	return fmt.Errorf("SteamCMD app %d: %w", appID, lastErr)
}

// Login signs in to Steam interactively so SteamCMD caches the session.
// SteamCMD asks for the password and any Steam Guard code itself, so
// Anyserve never sees or stores them.
func (s SteamCMD) Login(ctx context.Context, user string) error {
	exe, err := s.Ensure(ctx)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, exe, "+login", user, "+quit")
	cmd.Dir = s.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Dir32 and Dir64 hold SteamCMD's own steamclient libraries.
func (s SteamCMD) Dir32() string { return filepath.Join(s.Dir, "linux32") }
func (s SteamCMD) Dir64() string { return filepath.Join(s.Dir, "linux64") }

var successRe = regexp.MustCompile(`Success! App '\d+' (fully installed|already up to date)`)

func (s SteamCMD) run(ctx context.Context, exe string, args []string) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = s.Dir
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return err
	}
	ok := false
	var tail []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if s.Output != nil {
				fmt.Fprintln(s.Output, line)
			}
			if successRe.MatchString(line) {
				ok = true
			}
			if strings.Contains(line, "Update state") || strings.Contains(line, "ERROR") || strings.HasPrefix(line, "Success") {
				s.Log("  %s", strings.TrimSpace(line))
			}
			tail = append(tail, line)
			if len(tail) > 15 {
				tail = tail[1:]
			}
		}
	}()
	err := cmd.Wait()
	pw.Close()
	<-done
	if ok {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("SteamCMD didn't report success")
	}
	return fmt.Errorf("%w; last output:\n%s", err, strings.Join(tail, "\n"))
}
