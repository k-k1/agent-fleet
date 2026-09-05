package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consoleCatalog returns one locale's Console catalog as a single string, for checking
// whether a key exists.
//
// The catalog is split by domain into `locales/<locale>/*.ts` (ADR 0067 decision 4), and
// `locales/<locale>.ts` is only a composition file of imports and spreads. Reading that one
// gives a check that says "missing" about keys that are present, so go through this
// function.
//
// Skips when the catalog is absent, for builds from a distribution without console/.
func consoleCatalog(t *testing.T, locale string) string {
	t.Helper()
	dir := filepath.Join("..", "..", "console", "src", "lib", "i18n", "locales", locale)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("catalog not available (%v)", err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		b.Write(raw)
		b.WriteString("\n")
		n++
	}
	// Zero files means "could not read", not "the key is missing". Passing silently would
	// make this check the same as not existing.
	if n == 0 {
		t.Fatalf("no .ts files at all in %s (did the catalog move?)", dir)
	}
	return b.String()
}

// consoleCatalogHasKey reports whether "key" is defined in the catalog.
func consoleCatalogHasKey(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}
