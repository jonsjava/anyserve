package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jonsjava/anyserve/internal/fetch"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
	"github.com/jonsjava/anyserve/internal/target"
)

// Scheduled-update policies.
const (
	PolicyOff    = "off"
	PolicyNotify = "notify"
	PolicyApply  = "apply"
)

// Policies lists the policies in the order the CLI shows them.
var Policies = []string{PolicyOff, PolicyNotify, PolicyApply}

// Policy returns an instance's scheduled-update policy.
func Policy(in *instance.Instance) string {
	if in.AutoUpdate == "" {
		return PolicyNotify
	}
	return in.AutoUpdate
}

// SetPolicy changes an instance's scheduled-update policy.
func (a *App) SetPolicy(name, policy string) error {
	ok := false
	for _, p := range Policies {
		ok = ok || p == policy
	}
	if !ok {
		return fmt.Errorf("policy must be one of %s (got %q)", strings.Join(Policies, ", "), policy)
	}
	in, d, err := a.Store.Load(name)
	if err != nil {
		return err
	}
	in.AutoUpdate = policy
	return a.Store.Save(in, d)
}

// DefinitionChanged reports whether two definitions differ in anything,
// not only their version: registry maintenance such as a new image digest
// may keep the version.
func DefinitionChanged(old, new *spec.Definition) bool { return defHash(old) != defHash(new) }

func defHash(d *spec.Definition) string {
	b, _ := json.Marshal(d)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Available describes an update waiting for one instance.
type Available struct {
	// Definition is set when the registry has a changed definition.
	Definition *spec.Definition
	// Build is set when Steam has a newer build ("old → new").
	FromBuild, ToBuild string
	// Notes explain anything that was skipped (held updates, newer
	// definitions this Anyserve can't use, checks that failed).
	Notes []string
}

// Any reports whether there's something to apply.
func (av Available) Any() bool { return av.Definition != nil || av.ToBuild != "" }

// Describe says what's available in a few words.
func (av Available) Describe(in *instance.Instance) string {
	var parts []string
	if av.Definition != nil {
		if av.Definition.Version == in.DefinitionVersion {
			parts = append(parts, "definition "+av.Definition.Version+" (updated)")
		} else {
			parts = append(parts, "definition "+in.DefinitionVersion+" → "+av.Definition.Version)
		}
	}
	if av.ToBuild != "" {
		parts = append(parts, "Steam build "+av.FromBuild+" → "+av.ToBuild)
	}
	return strings.Join(parts, ", ")
}

// CheckUpdate looks for a newer definition and, for SteamCMD servers, a
// newer Steam build. It uses the cached registry index; call Refresh first
// for the latest.
func (a *App) CheckUpdate(ctx context.Context, in *instance.Instance, d *spec.Definition) Available {
	var av Available
	e, err := a.Find(in.Registry + "/" + in.Service)
	var newer *NeedsNewerError
	switch {
	case errors.As(err, &newer):
		av.Notes = append(av.Notes, newer.Error())
	case err != nil:
		av.Notes = append(av.Notes, "registry: "+err.Error())
	case DefinitionChanged(d, e.Def):
		if in.HeldDefinition == defHash(e.Def) {
			av.Notes = append(av.Notes, "definition "+e.Def.Version+" is held: it failed its health check before (`anyserve update "+in.Name+"` tries it anyway)")
		} else {
			av.Definition = e.Def
		}
	}
	if sc := d.Install.SteamCMD; sc != nil && !d.IsStack() {
		have := fetch.InstalledBuild(in.ServerDir, sc.AppID)
		if have == "" {
			av.Notes = append(av.Notes, "can't tell which Steam build is installed")
			return av
		}
		user := ""
		if sc.Login == "required" {
			if user, err = a.steamUser(); err != nil {
				av.Notes = append(av.Notes, err.Error())
				return av
			}
		}
		latest, err := a.steamCMD().PublicBuild(ctx, sc.AppID, sc.Beta, user)
		switch {
		case err != nil:
			av.Notes = append(av.Notes, "Steam build check failed: "+err.Error())
		case latest == have:
		case latest == in.HeldBuild:
			av.Notes = append(av.Notes, "Steam build "+latest+" is held: it failed its health check before")
		default:
			av.FromBuild, av.ToBuild = have, latest
		}
	}
	return av
}

// AutoUpdateOptions controls one scheduled-update run.
type AutoUpdateOptions struct {
	// Names limits the run to these instances; empty means all.
	Names []string
	// DryRun checks and reports without applying anything.
	DryRun bool
	// Warn tells players this long before stopping.
	Warn time.Duration
	// Health is how long an updated server has to become healthy and stay
	// healthy for Settle before it's rolled back.
	Health, Settle time.Duration
	// NoRefresh skips fetching the registry index first.
	NoRefresh bool
}

// UpdateResult is what happened to one instance.
type UpdateResult struct {
	Name, Policy string
	Available    string
	// Outcome is one of: up to date, available, updated, rolled back,
	// failed, skipped.
	Outcome string
	Detail  string
	Checked time.Time
}

// AutoUpdate checks every instance (or opts.Names) and acts on each one's
// policy: off skips it, notify records what's available, apply backs up,
// updates, waits for the server to be healthy and rolls back if it isn't.
func (a *App) AutoUpdate(ctx context.Context, opts AutoUpdateOptions) ([]UpdateResult, error) {
	if opts.Health <= 0 {
		opts.Health = 10 * time.Minute
	}
	if opts.Settle <= 0 {
		opts.Settle = time.Minute
	}
	if !opts.NoRefresh {
		if err := a.Refresh(ctx); err != nil {
			a.Log("Couldn't refresh the registry (%v); checking against the cached index", err)
		}
	}
	list, broken, err := a.Store.List()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range opts.Names {
		want[n] = true
	}
	var results []UpdateResult
	for _, in := range list {
		if len(want) > 0 && !want[in.Name] {
			continue
		}
		delete(want, in.Name)
		r := a.autoUpdateOne(ctx, in.Name, opts)
		results = append(results, r)
	}
	for n := range want {
		if err, ok := broken[n]; ok {
			results = append(results, UpdateResult{Name: n, Outcome: "failed", Detail: err.Error(), Checked: time.Now()})
			continue
		}
		return results, fmt.Errorf("%w %q", instance.ErrNotFound, n)
	}
	if err := a.saveUpdateState(results); err != nil {
		a.Log("Couldn't save update state: %v", err)
	}
	a.notify(results)
	return results, nil
}

func (a *App) autoUpdateOne(ctx context.Context, name string, opts AutoUpdateOptions) UpdateResult {
	r := UpdateResult{Name: name, Checked: time.Now().UTC().Truncate(time.Second)}
	in, d, err := a.Store.Load(name)
	if err != nil {
		r.Outcome, r.Detail = "failed", err.Error()
		return r
	}
	r.Policy = Policy(in)
	if r.Policy == PolicyOff {
		r.Outcome = "skipped"
		r.Detail = "auto-update is off"
		return r
	}
	av := a.CheckUpdate(ctx, in, d)
	r.Detail = strings.Join(av.Notes, "; ")
	if !av.Any() {
		r.Outcome = "up to date"
		return r
	}
	r.Available = av.Describe(in)
	if r.Policy == PolicyNotify || opts.DryRun {
		r.Outcome = "available"
		return r
	}
	a.Log("Updating %s: %s", name, r.Available)
	outcome, detail := a.applyUpdate(ctx, name, av, opts)
	r.Outcome = outcome
	r.Detail = strings.Trim(strings.Join([]string{detail, r.Detail}, "; "), "; ")
	return r
}

// applyUpdate backs up, updates and health-checks one instance, rolling
// back if the update fails or the server doesn't become healthy.
func (a *App) applyUpdate(ctx context.Context, name string, av Available, opts AutoUpdateOptions) (string, string) {
	old, oldD, t, err := a.load(name)
	if err != nil {
		return "failed", err.Error()
	}
	wasRunning := t.Status(old).State == "running"
	backup := ""
	if len(oldD.Backup.Paths) > 0 {
		b, err := a.MakeBackup(name)
		switch {
		case err == nil:
			backup = b.Path
		case strings.Contains(err.Error(), "nothing to back up yet"):
			a.Log("Nothing to back up yet; updating without a backup")
		default:
			return "failed", "not updated, because the backup failed: " + err.Error()
		}
	}
	held := func(o *instance.Instance) {
		if av.Definition != nil {
			o.HeldDefinition = defHash(av.Definition)
		}
		if av.ToBuild != "" {
			o.HeldBuild = av.ToBuild
		}
	}
	if err := a.Update(ctx, name, opts.Warn, false); err != nil {
		why := "update failed: " + err.Error()
		return a.rollback(ctx, old, oldD, backup, wasRunning, held, why)
	}
	if !wasRunning {
		return "updated", "not running, so not health-checked"
	}
	if err := a.waitHealthy(ctx, name, opts.Health, opts.Settle); err != nil {
		return a.rollback(ctx, old, oldD, backup, wasRunning, held, "not healthy after updating: "+err.Error())
	}
	return "updated", ""
}

// rollback puts back the instance record and definition from before an
// update, re-creates its service, restores the pre-update backup and holds
// the failed update so it isn't retried.
func (a *App) rollback(ctx context.Context, old *instance.Instance, oldD *spec.Definition, backup string, wasRunning bool, hold func(*instance.Instance), why string) (string, string) {
	a.Log("Rolling back %s: %s", old.Name, why)
	t, err := target.Get(old.Target)
	if err != nil {
		return "failed", why + "; rollback: " + err.Error()
	}
	if st := t.Status(old).State; st != "stopped" {
		if err := t.Stop(old); err != nil {
			a.Log("Stopping %s: %v", old.Name, err)
		}
	}
	hold(old)
	if err := a.Store.Save(old, oldD); err != nil {
		return "failed", why + "; rollback: " + err.Error()
	}
	var notes []string
	if oldD.IsStack() {
		// Re-render config files and pinned downloads for the old
		// definition; the compose file then names the old image digests.
		if err := a.installFiles(ctx, old, oldD, false); err != nil {
			notes = append(notes, "rewriting files: "+err.Error())
		}
	} else if oldD.Install.SteamCMD != nil || oldD.Install.Archive != nil {
		notes = append(notes, "the server's program files stay at the new version (only its data is rolled back)")
	}
	env := a.targetEnv()
	env.Out = nil
	if err := t.Register(old, oldD, env); err != nil {
		return "failed", why + "; rollback: " + err.Error()
	}
	if backup != "" {
		if err := a.Restore(old.Name, backup); err != nil {
			notes = append(notes, "restoring data: "+err.Error())
		}
	}
	if wasRunning {
		if err := t.Start(old); err != nil {
			notes = append(notes, "starting: "+err.Error())
		}
	}
	detail := why
	if len(notes) > 0 {
		detail += "; " + strings.Join(notes, "; ")
	}
	return "rolled back", detail
}

// waitHealthy waits until an instance has been healthy for settle, giving
// up after limit.
func (a *App) waitHealthy(ctx context.Context, name string, limit, settle time.Duration) error {
	in, _, t, err := a.load(name)
	if err != nil {
		return err
	}
	return waitFor(ctx, func() target.Status { return t.Status(in) }, limit, settle, 5*time.Second)
}

func healthy(s target.Status) bool {
	return s.State == "running" && !strings.Contains(s.Detail, "(starting)")
}

func waitFor(ctx context.Context, status func() target.Status, limit, settle, every time.Duration) error {
	deadline := time.Now().Add(limit)
	var since time.Time
	last := target.Status{}
	for {
		last = status()
		if healthy(last) {
			if since.IsZero() {
				since = time.Now()
			}
			if time.Since(since) >= settle {
				return nil
			}
		} else {
			since = time.Time{}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still %s after %s %s", last.State, limit, last.Detail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

func (a *App) updateStatePath() string { return filepath.Join(a.Dirs.Data, "updates.json") }

// UpdateState returns the last scheduled-update result for each instance.
func (a *App) UpdateState() (map[string]UpdateResult, error) {
	out := map[string]UpdateResult{}
	b, err := os.ReadFile(a.updateStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	return out, json.Unmarshal(b, &out)
}

func (a *App) saveUpdateState(results []UpdateResult) error {
	state, err := a.UpdateState()
	if err != nil {
		state = map[string]UpdateResult{}
	}
	for _, r := range results {
		state[r.Name] = r
	}
	// Forget instances that were removed.
	for n := range state {
		if !a.Store.Exists(n) {
			delete(state, n)
		}
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.Dirs.Data, 0o700); err != nil {
		return err
	}
	return os.WriteFile(a.updateStatePath(), b, 0o600)
}

// notify posts a summary to the configured notify_url (an ntfy topic or
// any endpoint that accepts a plain-text POST), if anything happened.
func (a *App) notify(results []UpdateResult) {
	c, err := a.LoadConfig()
	if err != nil || c.NotifyURL == "" {
		return
	}
	var lines []string
	for _, r := range results {
		switch r.Outcome {
		case "available", "updated", "rolled back", "failed":
			l := r.Name + ": " + r.Outcome
			if r.Available != "" {
				l += " (" + r.Available + ")"
			}
			if r.Detail != "" && r.Outcome != "available" {
				l += " — " + r.Detail
			}
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return
	}
	sort.Strings(lines)
	host, _ := os.Hostname()
	req, err := http.NewRequest("POST", c.NotifyURL, bytes.NewBufferString(strings.Join(lines, "\n")))
	if err != nil {
		a.Log("Notify: %v", err)
		return
	}
	req.Header.Set("Title", "Anyserve updates on "+host)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	cl := &http.Client{Timeout: 20 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		a.Log("Notify: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		a.Log("Notify: %s answered %s", c.NotifyURL, resp.Status)
	}
}

// timerSupported reports whether scheduled updates can be installed here.
func timerSupported() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("scheduled updates need systemd for now; on %s, run `anyserve auto-update run` from your scheduler", runtime.GOOS)
	}
	return nil
}

// EnableSchedule installs a systemd user timer that runs
// `anyserve auto-update run` at the given OnCalendar time.
func (a *App) EnableSchedule(onCalendar string) error {
	if err := timerSupported(); err != nil {
		return err
	}
	return target.InstallUpdateTimer(a.targetEnv(), onCalendar)
}

// DisableSchedule removes the timer.
func (a *App) DisableSchedule() error {
	if err := timerSupported(); err != nil {
		return err
	}
	return target.RemoveUpdateTimer()
}

// Schedule describes the installed timer, or "" if there isn't one.
func (a *App) Schedule() string {
	if timerSupported() != nil {
		return ""
	}
	return target.UpdateTimerInfo()
}
