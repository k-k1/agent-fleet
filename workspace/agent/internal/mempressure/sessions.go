package mempressure

import (
	"errors"
	"io/fs"
	"sort"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/procx"
)

const (
	// MaxSessions and MaxProcs bound one per-session pass (ADR 0120 decision 8): smaps_rollup
	// walks page tables and briefly holds the target's mmap lock, so a pass touches at most
	// five sessions and fifty processes however many are alive.
	MaxSessions = 5
	MaxProcs    = 50

	// SkipFullAvg10 is the stage-2 level of `full avg10` (ADR 0120 decision 2) above which the
	// pass does not run at all: measuring must not add to the pressure it is measuring.
	SkipFullAvg10 = 5.0
)

// Live is one session the sampler could attribute processes to: a tmux session and the pid of
// its pane's root process.
type Live struct {
	Name string
	Kind string
	Root int
}

// Obs is one session's observation over ALL its attributed processes, or the reason it is
// missing. A budget that runs out part-way yields a missing observation, never a partial sum.
type Obs struct {
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	Procs    int     `json:"procs,omitempty"`
	PssAnon  *uint64 `json:"pss_anon,omitempty"`
	PssShmem *uint64 `json:"pss_shmem,omitempty"`
	Missing  string  `json:"missing,omitempty"`
}

// KindCoverage is how many sessions of a kind were alive and how many were measured in a pass.
type KindCoverage struct {
	Alive    int `json:"alive"`
	Measured int `json:"measured"`
}

// Pass is the result of one per-session pass.
type Pass struct {
	Skipped  string                  `json:"skipped,omitempty"`
	Coverage map[string]KindCoverage `json:"coverage,omitempty"`
	Obs      []Obs                   `json:"obs,omitempty"`
}

// rotor chooses which sessions a pass measures. It cycles through each kind's sessions in name
// order and takes one kind at a time, so a small session is measured as surely as a large one
// and no kind is starved by another having more sessions. The choice never looks at a footprint:
// a schedule that preferred big sessions would bias every percentile fixed from the series.
type rotor struct {
	last      map[string]string // kind → last name measured
	kindStart int
}

func (r *rotor) pick(live []Live, max int) []Live {
	byKind := map[string][]Live{}
	for _, l := range live {
		byKind[l.Kind] = append(byKind[l.Kind], l)
	}
	kinds := make([]string, 0, len(byKind))
	for k, ls := range byKind {
		sort.Slice(ls, func(i, j int) bool { return ls[i].Name < ls[j].Name })
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	if len(kinds) == 0 {
		return nil
	}
	if r.last == nil {
		r.last = map[string]string{}
	}
	start := r.kindStart % len(kinds)
	r.kindStart++
	taken := map[string]bool{}
	var out []Live
	for len(out) < max {
		progress := false
		for i := range kinds {
			if len(out) >= max {
				break
			}
			k := kinds[(start+i)%len(kinds)]
			if l, ok := nextAfter(byKind[k], r.last[k], taken); ok {
				taken[l.Name] = true
				r.last[k] = l.Name
				out = append(out, l)
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	return out
}

// nextAfter returns the first not-yet-taken session whose name sorts after last, wrapping round.
func nextAfter(ls []Live, last string, taken map[string]bool) (Live, bool) {
	for pass := 0; pass < 2; pass++ {
		for _, l := range ls {
			if taken[l.Name] {
				continue
			}
			if pass == 0 && l.Name <= last {
				continue
			}
			return l, true
		}
	}
	return Live{}, false
}

// tree returns root and its live descendants.
func tree(root int, kids map[int][]int, tab map[int]procx.Info) []int {
	if _, ok := tab[root]; !ok {
		return nil
	}
	seen := map[int]bool{root: true}
	out := []int{}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if tab[pid].State != 'Z' {
			out = append(out, pid)
		}
		for _, c := range kids[pid] {
			if !seen[c] {
				seen[c] = true
				queue = append(queue, c)
			}
		}
	}
	return out
}

// runPass measures the chosen sessions. rollup reads /proc/<pid>/smaps_rollup.
func (r *rotor) runPass(live []Live, tab map[int]procx.Info, rollup func(pid int) (string, error)) Pass {
	p := Pass{Coverage: map[string]KindCoverage{}}
	for _, l := range live {
		c := p.Coverage[l.Kind]
		c.Alive++
		p.Coverage[l.Kind] = c
	}
	kids := procx.Children(tab)
	used := 0
	for _, l := range r.pick(live, MaxSessions) {
		o := Obs{Name: l.Name, Kind: l.Kind}
		pids := tree(l.Root, kids, tab)
		switch {
		case len(pids) == 0:
			o.Missing = "no_process"
		case used+len(pids) > MaxProcs:
			o.Missing = "budget"
		default:
			used += len(pids)
			o.Procs = len(pids)
			var anon, shmem uint64
			for _, pid := range pids {
				txt, err := rollup(pid)
				if errors.Is(err, fs.ErrNotExist) {
					continue // exited since the table was read
				}
				if err != nil {
					o.Missing = "unreadable"
					break
				}
				a, s, err := ParseRollup(txt)
				if err != nil {
					o.Missing = "unparsable"
					break
				}
				anon, shmem = anon+a, shmem+s
			}
			if o.Missing == "" {
				o.PssAnon, o.PssShmem = &anon, &shmem
			} else {
				o.Procs = 0
			}
		}
		if o.Missing == "" {
			c := p.Coverage[l.Kind]
			c.Measured++
			p.Coverage[l.Kind] = c
		}
		p.Obs = append(p.Obs, o)
	}
	return p
}
