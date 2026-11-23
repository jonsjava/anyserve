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

// PublicBuild asks Steam for the build id currently published on an app's
// branch ("public" unless a beta is named), without downloading the app.
// user is "" for anonymous.
func (s SteamCMD) PublicBuild(ctx context.Context, appID int, branch, user string) (string, error) {
	exe, err := s.Ensure(ctx)
	if err != nil {
		return "", err
	}
	if user == "" {
		user = "anonymous"
	}
	if branch == "" {
		branch = "public"
	}
	cmd := exec.CommandContext(ctx, exe, "+login", user, "+app_info_update", "1", "+app_info_print", strconv.Itoa(appID), "+quit")
	cmd.Dir = s.Dir
	out, err := cmd.CombinedOutput()
	if id := branchBuild(string(out), branch); id != "" {
		return id, nil
	}
	if err == nil {
		err = fmt.Errorf("no build id for branch %q in SteamCMD's app info", branch)
	}
	return "", fmt.Errorf("SteamCMD app info for %d: %w", appID, err)
}

// branchBuild finds depots → branches → <branch> → buildid in SteamCMD's
// app_info_print output (Valve's KeyValues text format).
func branchBuild(text, branch string) string {
	i := strings.Index(text, `"branches"`)
	if i < 0 {
		return ""
	}
	toks := vdfTokens(text[i+len(`"branches"`):])
	// toks: { "public" { "buildid" "123" ... } "beta" { ... } }
	depth := 0
	current := ""
	for j := 0; j < len(toks); j++ {
		switch t := toks[j]; t {
		case "{":
			depth++
		case "}":
			depth--
			if depth == 0 {
				return ""
			}
			if depth == 1 {
				current = ""
			}
		default:
			if depth == 1 && current == "" && j+1 < len(toks) && toks[j+1] == "{" {
				current = t
				continue
			}
			if depth == 2 && current == branch && t == "buildid" && j+1 < len(toks) {
				return toks[j+1]
			}
			if depth == 2 && j+1 < len(toks) && toks[j+1] != "{" && toks[j+1] != "}" {
				j++ // skip the value
			}
		}
	}
	return ""
}

func vdfTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '{', '}':
			out = append(out, string(c))
		case '"':
			j := i + 1
			var b strings.Builder
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
			}
			out = append(out, b.String())
			i = j
		}
	}
	return out
}

var manifestBuildRe = regexp.MustCompile(`"buildid"\s+"(\d+)"`)

// InstalledBuild reads the build id SteamCMD recorded when it last
// installed an app into dir, or "" if it can't tell.
func InstalledBuild(dir string, appID int) string {
	b, err := os.ReadFile(filepath.Join(dir, "steamapps", fmt.Sprintf("appmanifest_%d.acf", appID)))
	if err != nil {
		return ""
	}
	if m := manifestBuildRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}
