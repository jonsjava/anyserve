package target

import (
	"fmt"
	"io"

	"github.com/jonsjava/anyserve/internal/instance"
	"github.com/jonsjava/anyserve/internal/spec"
)

// planned is a placeholder for a target that's on the roadmap but not built
// yet. It's listed so users can see what's coming, and refuses to install.
type planned struct {
	name, desc, milestone string
}

func init() {
	register(&planned{"windows-service", "Windows: a Windows service", "milestone 4"})
	register(&planned{"launchd", "macOS: a launchd agent", "milestone 4"})
	register(&planned{"kubernetes", "Any OS: generated Kubernetes manifests", "milestone 6"})
}

func (p *planned) Name() string        { return p.name }
func (p *planned) Description() string { return p.desc + " (not built yet)" }
func (p *planned) Available() error {
	return fmt.Errorf("the %s target isn't built yet (planned for %s)", p.name, p.milestone)
}
func (p *planned) SupportsService(*spec.Definition) error { return p.Available() }
func (p *planned) Register(*instance.Instance, *spec.Definition, Env) error {
	return p.Available()
}
func (p *planned) Unregister(*instance.Instance) error { return nil }
func (p *planned) Start(*instance.Instance) error      { return p.Available() }
func (p *planned) Stop(*instance.Instance) error       { return p.Available() }
func (p *planned) Status(*instance.Instance) Status {
	return Status{State: "unknown", Detail: "target not built yet"}
}
func (p *planned) Logs(*instance.Instance, int, bool, io.Writer) error { return p.Available() }
