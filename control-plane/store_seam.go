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

// ⚠️ A closure, not the function value. awsConfigFor dispatches per call into
// runtime.AWSConfigFor — the variable the live AWS harness swaps to point a whole run at
// a test account (docs/log/64 §64.22.3 / §64.23). Assigning the function value here would
// resolve it at package init, and the swap would then reach the runtime adapters but not
// the store: a split-brain nobody would see, because every call still succeeds.
func init() {
	store.AWSConfigFor = func(ctx context.Context, region string) (aws.Config, error) {
		return awsConfigFor(ctx, region)
	}
}
