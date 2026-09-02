package chatx

// アシスタント会話 slug（docs/log/38 アシスタント発火）: 形式・採番・解決・バックフィル。

import "testing"

func TestValidConvSlug(t *testing.T) {
	for s, want := range map[string]bool{
		"a3k7f2q":  true,
		"abcdefg":  true,
		"sbk7oej":  false, // session slug ("s" prefix)
		"a3k7f2":   false, // too short
		"a3k7f2qq": false,
		"a3K7F2Q":  false, // upper case
		"a3k7f18":  false, // 0/1 are not base32 chars
		"":         false,
	} {
		if got := validConvSlug(s); got != want {
			t.Errorf("validConvSlug(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestRandConvSlugShape(t *testing.T) {
	for i := 0; i < 50; i++ {
		if s := randConvSlug(); !validConvSlug(s) {
			t.Fatalf("randConvSlug() = %q is not a valid slug", s)
		}
	}
}

func TestAllocConvSlugAvoidsTaken(t *testing.T) {
	taken := map[string]bool{}
	for i := 0; i < 200; i++ {
		s := allocConvSlug(taken)
		if taken[s] {
			t.Fatalf("allocConvSlug returned taken slug %q", s)
		}
		taken[s] = true
	}
}

func TestConvSlugBackfillAndResolve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A pre-slug conversation on disk (written directly, as old code did).
	old := &chatConversation{ID: randUUID(), Title: "old", Messages: []chatMessage{}}
	if err := saveConv(old); err != nil {
		t.Fatal(err)
	}
	backfillConvSlugs()

	c, err := loadConv(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !validConvSlug(c.Slug) {
		t.Fatalf("backfill did not assign a valid slug: %q", c.Slug)
	}

	// resolveConvRef: slug → id, id → id, garbage → miss.
	if id, ok := resolveConvRef(c.Slug); !ok || id != old.ID {
		t.Fatalf("resolve by slug = (%q,%v), want (%q,true)", id, ok, old.ID)
	}
	if id, ok := resolveConvRef(old.ID); !ok || id != old.ID {
		t.Fatalf("resolve by uuid = (%q,%v), want (%q,true)", id, ok, old.ID)
	}
	if _, ok := resolveConvRef("a999999"); ok {
		t.Fatal("unknown slug must not resolve")
	}
	if _, ok := resolveConvRef("not-a-ref"); ok {
		t.Fatal("garbage must not resolve")
	}

	// Backfill is idempotent: a second run keeps the assigned slug.
	backfillConvSlugs()
	c2, _ := loadConv(old.ID)
	if c2.Slug != c.Slug {
		t.Fatalf("backfill must be idempotent: %q -> %q", c.Slug, c2.Slug)
	}

	// The list surface carries the slug (Console / schedule tooling reads it there).
	metas, err := listConvs()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Slug != c.Slug {
		t.Fatalf("listConvs slug = %+v, want slug %q", metas, c.Slug)
	}
}
