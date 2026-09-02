package store

// テストだけが使う、main 側ヘルパの写し。util.go（製品コードの切断面）と分けて
// あるのは、これらを store の製品面に足す理由が無いため。
//
// ⚠️ util.go と同じ一時的な重複である。ウェーブ境界で共有ヘルパへまとめるとき、
// ここも一緒に片付けること。

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

// gib mirrors main.gib (mem.go). Untyped, so it flexes at each use site.
const gib = 1024 * 1024 * 1024

var userInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeUser mirrors main.sanitizeUser (resolver.go): an email (or any id)
// turned into a container-name-safe key.
func sanitizeUser(s string) string {
	s = userInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// main 側の定数の写し。値そのものがテストの前提（provider id は行に入る文字列、
// trust は tenant_idp の列、lease はリース期限の計算式）なので、定義元がずれたら
// ここも赤くなってほしい——が、いまは黙ってずれる。ウェーブ境界で 1 つにまとめる。
const (
	googleProviderID = "google"        // oauth.go
	trustIssuer      = "issuer"        // oauth_oidc.go
	shareOwnerLease  = 2 * time.Minute // session_share.go
)

// countRows mirrors the helper in main's identity_link_test.go.
func countRows(t *testing.T, st *SQL, table string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// linkOf mirrors the helper in main's identity_link_test.go:「realm を持たない、
// ごく普通のログイン 1 回」。
func linkOf(provider, subject, email string, emailJoin bool) IdentityLink {
	return IdentityLink{
		Provider: provider, Subject: subject, Email: email,
		FallbackKey: sanitizeUser(email), EmailJoin: emailJoin,
	}
}
