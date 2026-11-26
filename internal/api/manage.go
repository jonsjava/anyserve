package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/jonsjava/anyserve/internal/app"
	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

// Service is one installable service in the catalog.
type Service struct {
	Ref         string `json:"ref"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Registry    string `json:"registry"`
}

// InstallBody is the body of POST /v1/instances.
type InstallBody struct {
	Service  string            `json:"service"`
	Name     string            `json:"name"`
	Settings map[string]string `json:"settings,omitempty"`
	Ports    map[string]int    `json:"ports,omitempty"`
	// Expose opens these private ports to the network (stacks only).
	Expose []string `json:"expose,omitempty"`
	Domain string   `json:"domain,omitempty"`
	Start  bool     `json:"start,omitempty"`
	// DryRun checks the request and returns the plan, installing nothing.
	DryRun bool `json:"dry_run,omitempty"`
}

// PlanInfo is a checked install: the ports it will use (taken defaults
// already moved to free ones) and anything worth knowing first.
type PlanInfo struct {
	Name     string         `json:"name"`
	Target   string         `json:"target"`
	Ports    map[string]int `json:"ports"`
	Moved    []string       `json:"moved"`
	Warnings []string       `json:"warnings"`
}

// Job is a long-running change, such as an install.
type Job struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Instance string    `json:"instance"`
	Started  time.Time `json:"started"`
	Done     bool      `json:"done"`
	Error    string    `json:"error,omitempty"`
	Log      string    `json:"log"`

	owner string
	mu    sync.Mutex
	buf   bytes.Buffer
}

func (j *Job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.Write(p)
}

func (j *Job) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Job{ID: j.ID, Kind: j.Kind, Instance: j.Instance, Started: j.Started, Done: j.Done, Error: j.Error, Log: j.buf.String()}
}

func (s *Server) catalog(q *request) {
	entries, _, err := q.app.Catalog()
	if err != nil {
		fail(q.w, http.StatusInternalServerError, err, "")
		return
	}
	out := []Service{}
	for _, e := range entries {
		kind := e.Def.Kind
		if kind == "" {
			kind = spec.KindServer
		}
		out = append(out, Service{Ref: e.Registry + "/" + e.Def.ID, ID: e.Def.ID, Name: e.Def.Name, Description: e.Def.Description, Kind: kind, Registry: e.Registry})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(q.w, http.StatusOK, out)
}

// catalogItem returns a service's full definition, for building the
// install form.
func (s *Server) catalogItem(q *request) {
	e, err := q.app.Find(q.r.PathValue("registry") + "/" + q.r.PathValue("id"))
	if err != nil {
		fail(q.w, http.StatusNotFound, err, "")
		return
	}
	writeJSON(q.w, http.StatusOK, e.Def)
}

func (s *Server) install(q *request) {
	var b InstallBody
	if !decode(q, &b) {
		return
	}
	if b.Name == "" {
		fail(q.w, http.StatusBadRequest, errors.New("give the instance a name"), "")
		return
	}
	if err := instance.ValidateName(b.Name); err != nil {
		fail(q.w, http.StatusBadRequest, err, "")
		return
	}
	req := app.InstallRequest{Service: b.Service, Name: b.Name, Settings: b.Settings, Ports: b.Ports, Expose: b.Expose, Domain: b.Domain, Start: b.Start}
	plan, err := q.app.PlanInstall(req)
	if err != nil {
		fail(q.w, http.StatusBadRequest, err, q.out.String())
		return
	}
	info := PlanInfo{Name: b.Name, Target: plan.Target.Name(), Ports: plan.Ports, Moved: append([]string{}, plan.Moved...), Warnings: append([]string{}, plan.Warnings...)}
	if b.DryRun {
		writeJSON(q.w, http.StatusOK, info)
		return
	}
	v, _ := s.locks.LoadOrStore(b.Name, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		fail(q.w, http.StatusConflict, fmt.Errorf("%s is busy with another change", b.Name), "")
		return
	}
	j := s.newJob("install", b.Name, q.caller.Name)
	go func() {
		defer mu.Unlock()
		// The job outlives the request: closing the browser doesn't stop
		// an install halfway.
		a, err := s.NewApp(j)
		if err == nil {
			// Plan again with the job's App, so its output is the job's log.
			var p *app.Plan
			if p, err = a.PlanInstall(req); err == nil {
				_, err = a.Install(context.Background(), p)
			}
		}
		j.mu.Lock()
		j.Done = true
		if err != nil {
			j.Error = err.Error()
		}
		j.mu.Unlock()
		s.auditLine(q.caller, "job "+j.ID+" install "+b.Name, err)
	}()
	writeJSON(q.w, http.StatusAccepted, j.snapshot())
}

func (s *Server) newJob(kind, inst, owner string) *Job {
	b := make([]byte, 8)
	rand.Read(b)
	j := &Job{ID: hex.EncodeToString(b), Kind: kind, Instance: inst, Started: time.Now().UTC(), owner: owner}
	s.jobs.Store(j.ID, j)
	// Forget finished jobs after a day.
	time.AfterFunc(24*time.Hour, func() { s.jobs.Delete(j.ID) })
	return j
}

func (s *Server) job(q *request) {
	v, ok := s.jobs.Load(q.r.PathValue("id"))
	if !ok || v.(*Job).owner != q.caller.Name {
		fail(q.w, http.StatusNotFound, errors.New("no such job"), "")
		return
	}
	writeJSON(q.w, http.StatusOK, v.(*Job).snapshot())
}

// RemoveBody confirms a removal by repeating the instance's name.
type RemoveBody struct {
	Confirm string `json:"confirm"`
}

func (s *Server) remove(q *request) {
	var b RemoveBody
	if !decode(q, &b) {
		return
	}
	if b.Confirm != q.name {
		fail(q.w, http.StatusBadRequest, fmt.Errorf("type the instance's name (%s) to confirm", q.name), "")
		return
	}
	if err := q.app.Remove(q.name); err != nil {
		fail(q.w, http.StatusInternalServerError, err, q.out.String())
		return
	}
	// Drop the instance from users' grants, so a new instance with the same
	// name isn't handed to them.
	if cfg, err := q.app.LoadConfig(); err == nil {
		for _, u := range cfg.Users {
			q.app.Revoke(u.Name, q.name)
		}
	}
	writeJSON(q.w, http.StatusOK, Result{OK: true, Log: q.out.String()})
}

func (s *Server) auditLine(c app.Caller, what string, err error) {
	if s.Audit == nil {
		return
	}
	res := "ok"
	if err != nil {
		res = "failed: " + err.Error()
	}
	s.auditMu.Lock()
	fmt.Fprintf(s.Audit, "%s %s %s %s\n", time.Now().UTC().Format(time.RFC3339), c.Name, what, res)
	s.auditMu.Unlock()
}
