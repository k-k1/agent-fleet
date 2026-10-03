package store

import (
	"context"
	"testing"
	"time"
)

// One holder at a time, on both dialects: another CP is refused until the lease expires,
// the holder extends its own, and a release frees it at once.
func TestCPLeaseHasOneHolder(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			take := func(holder string, at time.Time) bool {
				t.Helper()
				ok, err := st.AcquireCPLease(ctx, "golden", holder, at, at.Add(3*time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				return ok
			}
			if !take("old-cp", now) {
				t.Fatal("a free lease was refused")
			}
			if take("new-cp", now.Add(time.Minute)) {
				t.Fatal("a second CP took a lease that had not expired")
			}
			if !take("old-cp", now.Add(2*time.Minute)) {
				t.Fatal("the holder could not extend its own lease")
			}
			if take("new-cp", now.Add(4*time.Minute)) {
				t.Fatal("the extension did not hold the lease past its first expiry")
			}
			if !take("new-cp", now.Add(6*time.Minute)) {
				t.Fatal("an expired lease was not taken over")
			}
			if take("old-cp", now.Add(6*time.Minute)) {
				t.Fatal("the old holder took it back from the new one")
			}
			if err := st.ReleaseCPLease(ctx, "golden", "old-cp"); err != nil {
				t.Fatal(err)
			}
			if take("third-cp", now.Add(7*time.Minute)) {
				t.Fatal("a release by somebody who does not hold it freed the lease")
			}
			if err := st.ReleaseCPLease(ctx, "golden", "new-cp"); err != nil {
				t.Fatal(err)
			}
			if !take("third-cp", now.Add(7*time.Minute)) {
				t.Fatal("a released lease was refused")
			}
		})
	}
}
