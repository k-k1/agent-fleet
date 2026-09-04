package chatx

// Slugs for assistant conversations (docs/log/38 §assistant triggering).
//
// Symmetrically to a session, which can be addressed by an "s" + 6 base32 chars slug, a
// conversation gets a short immutable "a" + 6 base32 chars id too. The UUID stays
// canonical for the storage file name and the provider integration, but it is too long
// for a human or an automation (a schedule's trigger target, an operator tool) to point
// at a conversation with. A slug is assigned at creation; conversations older than the
// field are filled in once by backfillConvSlugs at agent startup.

import (
	"crypto/rand"
	"encoding/base32"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"log"
	"strings"
)

// validConvSlug reports whether s is a well-formed conversation slug ("a" + 6
// lowercase base32 chars) — the assistant twin of session slugs ("s…").
func validConvSlug(s string) bool {
	if len(s) != 7 || s[0] != 'a' {
		return false
	}
	for _, r := range s[1:] {
		if !((r >= 'a' && r <= 'z') || (r >= '2' && r <= '7')) {
			return false
		}
	}
	return true
}

// randConvSlug mints a random conversation slug. Panics only if crypto/rand fails
// (same stance as randSlug — we cannot safely mint identities without entropy).
func randConvSlug() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("randConvSlug: " + err.Error())
	}
	enc := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
	return "a" + enc[:6]
}

// allocConvSlug returns a fresh slug unused by any existing conversation. taken is
// the set of slugs already assigned (caller builds it from listConvs, so allocation
// stays O(existing) even when called in a backfill loop).
func allocConvSlug(taken map[string]bool) string {
	for {
		s := randConvSlug()
		if !taken[s] {
			return s
		}
	}
}

// takenConvSlugs collects the slugs currently in use.
func takenConvSlugs() map[string]bool {
	taken := map[string]bool{}
	metas, err := ListConvs()
	if err != nil {
		return taken
	}
	for _, m := range metas {
		if m.Slug != "" {
			taken[m.Slug] = true
		}
	}
	return taken
}

// newConvSlug is the creation-site helper: one fresh slug against the live store.
func NewConvSlug() string { return allocConvSlug(takenConvSlugs()) }

// resolveConvRef maps a conversation reference — a UUID or a slug — onto the
// conversation's UUID. ok=false when the ref matches nothing.
func ResolveConvRef(ref string) (id string, ok bool) {
	ref = strings.TrimSpace(ref)
	if paths.ValidIDSegment(ref) {
		if _, err := LoadConv(ref); err == nil {
			return ref, true
		}
		return "", false
	}
	if !validConvSlug(ref) {
		return "", false
	}
	metas, err := ListConvs()
	if err != nil {
		return "", false
	}
	for _, m := range metas {
		if m.Slug == ref {
			return m.ID, true
		}
	}
	return "", false
}

// backfillConvSlugs assigns slugs to conversations created before the field existed.
// Run once at agent start (async): each un-slugged conversation is loaded under its
// lock, re-checked, stamped, and saved. Best-effort — a failure just logs; the next
// start retries.
func BackfillConvSlugs() {
	metas, err := ListConvs()
	if err != nil {
		return
	}
	taken := map[string]bool{}
	for _, m := range metas {
		if m.Slug != "" {
			taken[m.Slug] = true
		}
	}
	for _, m := range metas {
		if m.Slug != "" {
			continue
		}
		func() {
			unlock := LockConv(m.ID)
			defer unlock()
			c, err := LoadConv(m.ID)
			if err != nil || c.Slug != "" { // re-check under the lock
				return
			}
			c.Slug = allocConvSlug(taken)
			taken[c.Slug] = true
			if err := SaveConv(c); err != nil {
				log.Printf("chat: backfill slug %s: %v", m.ID, err)
			}
		}()
	}
}
