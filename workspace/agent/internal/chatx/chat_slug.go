package chatx

// アシスタント会話の slug（docs/log/38 アシスタント発火）。
//
// セッションが "s"+base32 6字の slug で呼べるのと対称に、会話にも "a"+base32 6字の
// 短い不変 ID を与える。UUID は保存ファイル名と provider 連携の正だが、人間や自動化
// （スケジュールの発火先・operator ツール）が会話を指すには長すぎる。slug は作成時に
// 採番し、フィールド導入前の既存会話は agent 起動時に backfillConvSlugs が一度だけ
// 補完する。

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
	metas, err := listConvs()
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
func newConvSlug() string { return allocConvSlug(takenConvSlugs()) }

// resolveConvRef maps a conversation reference — a UUID or a slug — onto the
// conversation's UUID. ok=false when the ref matches nothing.
func resolveConvRef(ref string) (id string, ok bool) {
	ref = strings.TrimSpace(ref)
	if paths.ValidIDSegment(ref) {
		if _, err := loadConv(ref); err == nil {
			return ref, true
		}
		return "", false
	}
	if !validConvSlug(ref) {
		return "", false
	}
	metas, err := listConvs()
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
func backfillConvSlugs() {
	metas, err := listConvs()
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
			unlock := lockConv(m.ID)
			defer unlock()
			c, err := loadConv(m.ID)
			if err != nil || c.Slug != "" { // re-check under the lock
				return
			}
			c.Slug = allocConvSlug(taken)
			taken[c.Slug] = true
			if err := saveConv(c); err != nil {
				log.Printf("chat: backfill slug %s: %v", m.ID, err)
			}
		}()
	}
}
