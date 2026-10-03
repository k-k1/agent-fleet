package store

import (
	"context"
	"testing"
	"time"
)

// One holder at a time, on both dialects, by the database's clock: no CP's own time enters
// the statement, so a CP whose clock runs ahead cannot see a live lease as expired.
func TestCPLeaseHasOneHolder(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		t.Run(name, func(t *testing.T) {
			check := func(what string, got bool, err error, want bool) {
				t.Helper()
				if err != nil {
					t.Fatalf("%s: %v", what, err)
				}
				if got != want {
					t.Fatalf("%s = %v, want %v", what, got, want)
				}
			}
			ok, err := st.AcquireCPLease(ctx, "golden", "old-cp", time.Hour)
			check("a free lease", ok, err, true)
			ok, err = st.AcquireCPLease(ctx, "golden", "new-cp", time.Millisecond)
			check("another CP while it is live", ok, err, false)
			ok, err = st.RenewCPLease(ctx, "golden", "new-cp", time.Hour)
			check("a renewal by a non-holder", ok, err, false)
			ok, err = st.RenewCPLease(ctx, "golden", "old-cp", 150*time.Millisecond)
			check("the holder's renewal", ok, err, true)
			ok, err = st.AcquireCPLease(ctx, "golden", "new-cp", time.Hour)
			check("another CP right after the renewal", ok, err, false)

			time.Sleep(300 * time.Millisecond)
			ok, err = st.RenewCPLease(ctx, "golden", "old-cp", time.Hour)
			check("a renewal after the lease expired", ok, err, false)
			ok, err = st.AcquireCPLease(ctx, "golden", "new-cp", time.Hour)
			check("an expired lease taken over", ok, err, true)
			ok, err = st.AcquireCPLease(ctx, "golden", "old-cp", time.Hour)
			check("the old holder taking it back", ok, err, false)
			ok, err = st.RenewCPLease(ctx, "golden", "old-cp", time.Hour)
			check("the old holder renewing it back", ok, err, false)
		})
	}
}
