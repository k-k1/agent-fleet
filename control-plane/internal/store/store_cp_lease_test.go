package store

import (
	"context"
	"sync"
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

// A released lease is free at once, and only its holder can release it.
func TestCPLeaseRelease(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		t.Run(name, func(t *testing.T) {
			if ok, err := st.AcquireCPLease(ctx, "home", "a", time.Hour); err != nil || !ok {
				t.Fatalf("acquire = %v, %v", ok, err)
			}
			if err := st.ReleaseCPLease(ctx, "home", "b"); err != nil {
				t.Fatal(err)
			}
			if ok, err := st.AcquireCPLease(ctx, "home", "b", time.Hour); err != nil || ok {
				t.Fatalf("taken after a non-holder's release = %v, %v; want still a's", ok, err)
			}
			if err := st.ReleaseCPLease(ctx, "home", "a"); err != nil {
				t.Fatal(err)
			}
			if ok, err := st.AcquireCPLease(ctx, "home", "b", time.Hour); err != nil || !ok {
				t.Fatalf("taken after the holder's release = %v, %v; want free", ok, err)
			}
		})
	}
}

// Every bump returns a value no other bump returned, on both dialects.
func TestCPCounterBumpsOnce(t *testing.T) {
	ctx := context.Background()
	for name, st := range homeOpStores(t) {
		t.Run(name, func(t *testing.T) {
			if v, err := st.CPCounter(ctx, "gen"); err != nil || v != 0 {
				t.Fatalf("unbumped = %d, %v; want 0", v, err)
			}
			const n = 8
			got := make(chan int64, n)
			var wg sync.WaitGroup
			for range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					v, err := st.BumpCPCounter(ctx, "gen")
					if err != nil {
						t.Error(err)
					}
					got <- v
				}()
			}
			wg.Wait()
			close(got)
			seen := map[int64]bool{}
			for v := range got {
				if seen[v] || v < 1 || v > n {
					t.Fatalf("bump returned %d twice or out of 1..%d", v, n)
				}
				seen[v] = true
			}
			if v, err := st.CPCounter(ctx, "gen"); err != nil || v != n {
				t.Fatalf("after %d bumps = %d, %v", n, v, err)
			}
			if v, err := st.CPCounter(ctx, "other"); err != nil || v != 0 {
				t.Fatalf("another counter = %d, %v; want 0", v, err)
			}
		})
	}
}
