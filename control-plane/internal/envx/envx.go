package envx

// Control Plane の環境変数パーサ。**1 実装 1 箇所**にするための小さなパッケージ。
//
// もとは control-plane/main.go にあり、ウェーブ B の CP-AUTH で internal/auth が
// 同じものを必要としたが、main.go はどのトラックの所有でもなかったため**写しが作られた**
// （ADR 0067 §1 ②・env.go の注記に「回収セッションが 1 本化すること」と明記されていた）。
// RECLAIM-B でその写しを畳み、main と internal/auth の両方がここを呼ぶ。
//
// ここに置くものの条件: **純粋で、設定を持たず、auth 固有でないこと。**
// （envBool は main.go にしか無かったので写しではない。ここへは移していない。）

import (
	"os"
	"strings"
	"time"
)

func Or(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func DurationOr(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
		return d
	}
	return def
}

// EmailSet parses a CSV of emails into a lowercased lookup set (SUPER_ADMIN_EMAILS).
func EmailSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			m[p] = true
		}
	}
	return m
}

// DomainSet parses a CSV of email domains into a lowercased lookup set, tolerating
// a leading "@" on each entry (AF_OAUTH_ALLOWED_DOMAINS).
func DomainSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(p)), "@")
		if p != "" {
			m[p] = true
		}
	}
	return m
}

// SplitCSV parses "A=1,B=2" into ["A=1","B=2"], dropping blanks.
func SplitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
