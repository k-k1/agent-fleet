// store_seam.go — the one thing the CP has to hand the store package at boot.
//
// The store's Secrets Manager reader (RDS managed-password rotation, docs/log/77) needs
// AWS credentials, but internal/store must not depend on internal/runtime to get them.
// So the store declares a seam and main fills it in, here.
//
// This is what is left of alias_store.go after the alias-collection pass (ADR 0067
// decision 2):
// every other line in that file was a name the CP can now spell as store.X directly.
package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Go through awsConfigFor (the function in runtime_seam.go), never runtime.AWSConfigFor
// itself. The latter is a variable the live AWS harness swaps to point one whole run at a
// test account (docs/log/64 §64.22.3 / §64.23), so writing
// `store.AWSConfigFor = runtime.AWSConfigFor` here would freeze the value as it stood at
// init: the swap would reach the runtime adapter (which re-reads the variable on every
// call) but never the store, and since every call still succeeds it is a split-brain
// nobody can see.
//
// awsConfigFor is a function, so assigning it keeps the indirection — its body reads
// runtime.AWSConfigFor each time. The closure below only makes that indirection visible;
// it is equivalent to `store.AWSConfigFor = awsConfigFor`.
func init() {
	store.AWSConfigFor = func(ctx context.Context, region string) (aws.Config, error) {
		return awsConfigFor(ctx, region)
	}
}
