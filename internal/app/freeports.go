package app

import (
	"fmt"
	"sort"

	"github.com/jonsjava/anyserve/internal/spec"
)

// pickFreePorts moves each default port the user didn't choose to the next
// free number when another instance or another program already has it, so
// installs just work. Ports that share a number (a game's TCP and UDP, say)
// move together. It returns a line per move.
func (a *App) pickFreePorts(name string, d *spec.Definition, given, ports map[string]int) []string {
	taken := map[int][]string{}
	others, _, _ := a.Store.List()
	for _, o := range others {
		if o.Name == name {
			continue
		}
		_, od, err := a.Store.Load(o.Name)
		if err != nil {
			continue
		}
		op, err := od.ResolvePorts(o.Ports)
		if err != nil {
			continue
		}
		for _, p := range od.Ports {
			taken[op[p.Name]] = append(taken[op[p.Name]], p.Protocol)
		}
	}
	if routes, err := a.Routes(); err == nil {
		for _, r := range routes {
			if r.Kind == spec.RouteMinecraft {
				taken[a.routerPort()] = append(taken[a.routerPort()], "tcp")
				break
			}
		}
	}
	// The user's own choices are fixed; claim them first.
	for _, p := range d.Ports {
		if _, ok := given[p.Name]; ok {
			taken[ports[p.Name]] = append(taken[ports[p.Name]], p.Protocol)
		}
	}
	// Group the ports left to place by their current number.
	groups := map[int][]spec.Port{}
	for _, p := range d.Ports {
		if _, ok := given[p.Name]; !ok {
			groups[ports[p.Name]] = append(groups[ports[p.Name]], p)
		}
	}
	nums := make([]int, 0, len(groups))
	for n := range groups {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	free := func(n int, g []spec.Port) bool {
		if n < 1 || n > 65535 {
			return false
		}
		for _, p := range g {
			for _, t := range taken[n] {
				if protocolsOverlap(norm(p.Protocol), norm(t)) {
					return false
				}
			}
			if busy(n, norm(p.Protocol)) {
				return false
			}
		}
		return true
	}
	var moved []string
	for _, n := range nums {
		g := groups[n]
		to := n
		for i := 0; !free(to, g) && i < 1000; i++ {
			to++
		}
		if !free(to, g) {
			to = n // leave it; checkPorts reports the clash
		}
		for _, p := range g {
			ports[p.Name] = to
			taken[to] = append(taken[to], p.Protocol)
			if to != n {
				moved = append(moved, fmt.Sprintf("port %s: %d is taken, using %d", p.Name, n, to))
			}
		}
	}
	return moved
}

func norm(p string) string {
	if p == "" {
		return "tcp"
	}
	return p
}
