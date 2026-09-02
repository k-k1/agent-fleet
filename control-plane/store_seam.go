// store_seam.go — the one thing the CP has to hand the store package at boot.
//
// The store's Secrets Manager reader (RDS managed-password rotation, docs/log/77) needs
// AWS credentials, but internal/store must not depend on internal/runtime to get them.
// So the store declares a seam and main fills it in, here.
//
// This is what is left of alias_store.go after the alias-collection pass (ADR 0067 決定 2):
// every other line in that file was a name the CP can now spell as store.X directly.
package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// ⚠️ 経由するのは awsConfigFor（runtime_seam.go の**関数**）であって
// runtime.AWSConfigFor そのものではない。後者はライブ AWS ハーネスが 1 回の実行まるごと
// テストアカウントへ向けるために差し替える**変数**（docs/log/64 §64.22.3 / §64.23）なので、
// ここに直に `store.AWSConfigFor = runtime.AWSConfigFor` と書くと **init 時点の値で凍り**、
// 差し替えは runtime アダプタ（毎回この変数を読む）にだけ届いて store には届かない
// —— どの呼び出しも成功したままなので誰にも見えない split-brain になる。
//
// awsConfigFor は関数なので、その値を代入しても本体が毎回 runtime.AWSConfigFor を読む
// （#298 で var から関数へ直された理由がこれ）。下のクロージャはその間接をひと目で
// 見えるようにしているだけで、`store.AWSConfigFor = awsConfigFor` と等価である。
func init() {
	store.AWSConfigFor = func(ctx context.Context, region string) (aws.Config, error) {
		return awsConfigFor(ctx, region)
	}
}
