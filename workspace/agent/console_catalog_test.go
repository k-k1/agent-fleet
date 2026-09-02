package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consoleCatalog は Console の 1 ロケール分のカタログを、キー存在確認のために
// 1 本の文字列として返す。
//
// ⚠️ カタログはドメイン別に `locales/<locale>/*.ts` へ分かれており（ADR 0067 決定 4）、
// `locales/<locale>.ts` は import と spread しか持たない**合成ファイル**である。
// そちらを読むと「キーが在るのに無い」と言う検査になる —— この関数を経由すること。
//
// console/ を含まない配布物でビルドする場合に備えて、カタログが無ければスキップする。
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
	// 0 件でも「キーが無い」ではなく「読めていない」。黙って通すと、この検査は
	// 存在しないのと同じになる。
	if n == 0 {
		t.Fatalf("%s に .ts が 1 つも無い（カタログの置き場所が変わった？）", dir)
	}
	return b.String()
}

// consoleCatalogHasKey は "key" がカタログに定義されているかを見る。
func consoleCatalogHasKey(catalog, key string) bool {
	return strings.Contains(catalog, `"`+key+`"`)
}
