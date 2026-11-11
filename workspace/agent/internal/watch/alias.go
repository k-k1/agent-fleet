package watch

import "strings"

// TypeAliaser is a Source that renamed an event type family and still accepts the old name.
// The map is old prefix -> new prefix ("github.workflow_run" -> "ci.run"); it applies to an exact
// type and to a ".*" pattern alike. The source emits only the new names, so one run is one event
// per type and a subscription that names both spellings is told once (it is de-duplicated by
// (type, version) like any other).
type TypeAliaser interface {
	TypeAliases() map[string]string
}

// canonPattern rewrites one type pattern through aliases; ok is false when nothing applied.
func canonPattern(p string, aliases map[string]string) (string, bool) {
	base, wild := strings.CutSuffix(p, ".*")
	for old, now := range aliases {
		if base == old || strings.HasPrefix(base, old+".") {
			out := now + strings.TrimPrefix(base, old)
			if wild {
				out += ".*"
			}
			return out, true
		}
	}
	return p, false
}

// canonPatterns returns patterns with aliases applied and duplicates removed (order kept).
func canonPatterns(patterns []string, aliases map[string]string) []string {
	if len(aliases) == 0 {
		return patterns
	}
	out := make([]string, 0, len(patterns))
	seen := map[string]bool{}
	for _, p := range patterns {
		p, _ = canonPattern(p, aliases)
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func aliasesOf(src Source) map[string]string {
	if a, ok := src.(TypeAliaser); ok {
		return a.TypeAliases()
	}
	return nil
}

// migrateAliasedLocked rewrites subscriptions stored under an old type name: their patterns, the
// versions already told, and the events still pending. Without the Told move a run that was
// delivered under the old name would be delivered again under the new one. When both names are
// present the new one wins.
func (e *Engine) migrateAliasedLocked() {
	for i := range e.state.Subs {
		s := &e.state.Subs[i]
		src, ok := e.sources[s.Source]
		if !ok {
			continue
		}
		al := aliasesOf(src)
		if len(al) == 0 {
			continue
		}
		s.Types = canonPatterns(s.Types, al)
		for k, v := range s.Told {
			if nk, changed := canonPattern(k, al); changed {
				if _, has := s.Told[nk]; !has {
					s.Told[nk] = v
				}
				delete(s.Told, k)
			}
		}
		var pend []Event
		for _, ev := range s.Pending {
			ev.Type, _ = canonPattern(ev.Type, al)
			dup := false
			for _, p := range pend {
				dup = dup || p.Type == ev.Type
			}
			if !dup {
				pend = append(pend, ev)
			}
		}
		s.Pending = pend
	}
}
