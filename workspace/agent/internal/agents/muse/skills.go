package muse

// skills.go is the native half of the session skill picker for muse (ADR 0095 P2-23): MSP's
// `skill/list`, asked of the session's own running host.
//
// It is per-SESSION and not a launch-time catalogue, which is why it does not live beside the
// model list: the wire says so ("per-session because skill scope follows the session's workspace
// and plugin state"), and the answer really does differ — a project skill under
// `.agents/skills/` belongs to one working copy. With no live host there is no session to ask
// about, so the answer is nil and the caller falls back to the foreign (injection) entries that
// served muse before this existed.
//
// No cache. The picker asks once when it opens, and this is a JSON-RPC round trip to a process
// that is already running — unlike the model catalogue, which may have to start a 299 MB one.
// A cache would also have to honour `skill/changed`, and a stale skill list is a picker that
// offers a skill the host will answer `skillNotFound` for.

import (
	"log"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
)

// Skill is one user-invocable shortcut of a live muse session.
type Skill struct {
	// Selector is the token a `skill` input part carries, without the leading slash: bare
	// (`plan`) or plugin-qualified (`acme:deploy`).
	Selector     string
	DisplayName  string
	Description  string
	ArgumentHint string
	// Source is the wire's scope: bundled | user | project | plugin. Open vocabulary — a
	// future value passes through rather than being dropped.
	Source string
}

// Skills returns what this session's host says the member can invoke. nil means "no native
// answer" — no live host, or a host that could not answer — never "this session has none".
func Skills(sessionName string) []Skill {
	h := handleFor(sessionName)
	if h == nil {
		return nil
	}
	h.mu.Lock()
	cl, sid := h.cl, h.sid
	h.mu.Unlock()
	if cl == nil || sid == "" {
		return nil
	}
	return skillsFrom(cl, sid)
}

func skillsFrom(cl *msp.Client, sid string) []Skill {
	var res msp.SkillListResult
	if err := cl.CallInto(msp.MethodSkillList, msp.SkillListParams{SessionID: sid}, callTimeout, &res); err != nil {
		// Not an error the member should see: the picker still has the foreign entries, and a
		// host mid-shutdown answering nothing is ordinary.
		log.Printf("muse: skill/list: %v", err)
		return nil
	}
	return dedupePluginSpellings(res.Skills)
}

// dedupePluginSpellings collapses the two rows a plugin skill contributes — its bare-name winner
// and the `<pluginId>:<skillId>` form — into the one the picker offers.
//
// Both spellings are valid input, so this is presentation, not correctness. The bare one wins
// when the host awarded it to this plugin; the qualified one stays when it did not, because a
// skill whose bare name ANOTHER plugin won has no other way in, and dropping it would hide a
// skill rather than a duplicate. The pair is identified by (pluginId, displayName) rather than
// by splitting the selector on ":", so a bare selector that merely contains a colon is not
// mistaken for a qualified spelling.
func dedupePluginSpellings(rows []msp.SkillCatalogEntry) []Skill {
	bare := map[string]bool{}
	for _, r := range rows {
		if r.PluginID != nil && !strings.Contains(r.Selector, ":") {
			bare[*r.PluginID+"\x00"+r.DisplayName] = true
		}
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		if r.Selector == "" {
			continue
		}
		if r.PluginID != nil && strings.Contains(r.Selector, ":") && bare[*r.PluginID+"\x00"+r.DisplayName] {
			continue
		}
		s := Skill{
			Selector:    r.Selector,
			DisplayName: r.DisplayName,
			Description: r.Description,
			Source:      string(r.Source),
		}
		if r.ArgumentHint != nil {
			s.ArgumentHint = *r.ArgumentHint
		}
		if s.DisplayName == "" {
			s.DisplayName = s.Selector
		}
		out = append(out, s)
	}
	return out
}

// skillPart rewrites a leading `/selector` in the turn's first text part into a `skill` part.
//
// 🔴 It is required, not a nicety. The composer sends what the member typed, and over MSP a
// leading slash is just text: the typed dispatch that expands it is the host's, reached only
// through a `skill` part whose selector the host resolves (the TUI does the same resolution on
// its own side). Without this, picking a skill from the picker would send the model the literal
// string "/plan" and nothing would be expanded.
//
// The selector must be one the session actually has, which is why this asks `skill/list` rather
// than trusting the leading slash: a member whose message starts with "/tmp/notes.md is stale"
// must keep their text. That check costs one round trip, and only on a message starting with a
// slash.
func skillPart(cl *msp.Client, sid string, parts []msp.TurnInputPart) []msp.TurnInputPart {
	if len(parts) == 0 || parts[0].Type != msp.TurnInputPartTypeText || parts[0].Text == nil {
		return parts
	}
	text := strings.TrimLeft(*parts[0].Text, " \t")
	if !strings.HasPrefix(text, "/") {
		return parts
	}
	token, rest, _ := strings.Cut(strings.TrimPrefix(text, "/"), " ")
	if token == "" || !validSelector(token) {
		return parts
	}
	found := false
	for _, s := range skillsFrom(cl, sid) {
		if s.Selector == token {
			found = true
			break
		}
	}
	if !found {
		return parts
	}
	part := msp.TurnInputPart{Type: msp.TurnInputPartTypeSkill, Selector: strPtr(token)}
	if args := strings.TrimSpace(rest); args != "" {
		part.Arguments = strPtr(args)
	}
	out := append([]msp.TurnInputPart{part}, parts[1:]...)
	return out
}

// validSelector keeps the round trip off obvious non-selectors — a path, a URL, a sentence.
// The wire does not publish a grammar, so this is deliberately loose: it rejects what cannot be
// a shortcut token rather than defining what one is.
func validSelector(token string) bool {
	if token == "" || len(token) > 128 {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == ':':
		default:
			return false
		}
	}
	return true
}
