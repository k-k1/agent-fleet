package store

// 切断面のヘルパ。ここにあるのは、store 家系を `package main` から切り出したときに
// 「外向きに 1 本だけ伸びていた」小さな純関数である（ADR 0067 の仕分けでいう
// 「一緒に移す」）。定義元（`main.go` / `oauth_bitbucket.go` / `tenant_login.go`）は
// 他トラックの所有ファイルなので、このウェーブでは触らずに写している。
//
// ⚠️ 一時的な重複である。エイリアス回収と同じウェーブ境界で、共有ヘルパの置き場
// （`internal/…`）へ 1 つにまとめること。それまでは、どちらか片方だけを直すと
// 無言でずれる。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
)

// AWSConfigFor is the store family's single door to AWS credentials, used by the
// Secrets Manager lookup behind the Postgres password (store_postgres_secret.go).
//
// It is a variable for the same reason `awsConfigFor` in the owning binary is one:
// the live E2E has to run the PRODUCT under a copy of the CP task role while its own
// verification calls keep the deployer's ambient credentials (docs/log/64 §64.22.3).
// `control-plane/store_seam.go` therefore points this at that variable through a
// closure — resolved at call time, so overriding it in a test still reaches here.
// The default below only matters if nobody wires it.
var AWSConfigFor = func(ctx context.Context, region string) (aws.Config, error) {
	return awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
}

func awsConfigFor(ctx context.Context, region string) (aws.Config, error) {
	return AWSConfigFor(ctx, region)
}

// envOr mirrors main.envOr.
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// randHex mirrors main.randHex (oauth_bitbucket.go).
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// splitCSV mirrors main.splitCSV.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitCSVLower mirrors main.splitCSVLower (tenant_login.go): lowercased, deduped.
func splitCSVLower(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// splitDomainCSV mirrors main.splitDomainCSV (tenant_login.go): splitCSVLower for
// email domains, tolerating a leading "@".
func splitDomainCSV(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(p)), "@")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
