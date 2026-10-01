package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ssmStores opens SQLite always and Postgres when AF_TEST_DATABASE_URL is set. Postgres
// gets a schema of its own (search_path) rather than resetting public, so this can run
// while another package's Postgres test is resetting public on the same database.
func ssmStores(t *testing.T) map[string]*SQL {
	t.Helper()
	ctx := context.Background()
	lite, err := OpenSQLite(filepath.Join(t.TempDir(), "cp.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { lite.Close() })
	if err := lite.Migrate(ctx); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	out := map[string]*SQL{"sqlite": lite}
	url := os.Getenv("AF_TEST_DATABASE_URL")
	if url == "" {
		return out
	}
	schema := "t_ssm_" + strings.ToLower(NewID()[:8])
	admin, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { admin.db.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pg, err := OpenPostgres(url + sep + "search_path=" + schema)
	if err != nil {
		t.Fatalf("open postgres schema: %v", err)
	}
	t.Cleanup(func() { pg.Close() })
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate postgres: %v", err)
	}
	out["postgres"] = pg
	return out
}

// A profile that hosts still reference is not deleted, and the refusal names them: the
// column has no foreign key, so without this the hosts keep a dead id and every session
// start on them fails with bad_profile.
func TestDeleteSSMProfileRefusesWhileHostsUseIt(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			p := SSMProfile{ID: NewID(), MembershipID: "m1", Label: "prod", CreatedAt: NowTS()}
			spare := SSMProfile{ID: NewID(), MembershipID: "m1", Label: "spare", CreatedAt: NowTS()}
			for _, x := range []SSMProfile{p, spare} {
				if err := st.CreateSSMProfile(ctx, x); err != nil {
					t.Fatal(err)
				}
			}
			web := SSMHost{ID: NewID(), MembershipID: "m1", Alias: "web", ProfileID: p.ID, InstanceID: "i-1", CreatedAt: NowTS()}
			db := SSMHost{ID: NewID(), MembershipID: "m1", Alias: "db", ProfileID: p.ID, InstanceID: "i-2", CreatedAt: NowTS()}
			for _, h := range []SSMHost{web, db} {
				if err := st.CreateSSMHost(ctx, h); err != nil {
					t.Fatal(err)
				}
			}

			err := st.DeleteSSMProfile(ctx, p.ID, "m1")
			var inUse *SSMProfileInUseError
			if !errors.As(err, &inUse) {
				t.Fatalf("delete of a used profile: got %v, want *SSMProfileInUseError", err)
			}
			if got := aliases(inUse.Hosts); got != "db,web" {
				t.Fatalf("named hosts = %q, want db,web", got)
			}
			if _, found, _ := st.GetSSMProfile(ctx, p.ID); !found {
				t.Fatal("refused delete removed the profile")
			}

			// Another member cannot delete it either way, and gets no host list.
			if err := st.DeleteSSMProfile(ctx, p.ID, "m2"); err != nil {
				t.Fatalf("delete by another member: %v", err)
			}
			if _, found, _ := st.GetSSMProfile(ctx, p.ID); !found {
				t.Fatal("another member deleted the profile")
			}

			// Once no host points at it, the delete goes through.
			web.ProfileID, db.ProfileID = spare.ID, spare.ID
			if err := st.UpdateSSMHost(ctx, web); err != nil {
				t.Fatal(err)
			}
			if err := st.DeleteSSMHost(ctx, db.ID, "m1"); err != nil {
				t.Fatal(err)
			}
			if err := st.DeleteSSMProfile(ctx, p.ID, "m1"); err != nil {
				t.Fatalf("delete of an unused profile: %v", err)
			}
			if _, found, _ := st.GetSSMProfile(ctx, p.ID); found {
				t.Fatal("unused profile survived its delete")
			}
			if err := st.DeleteSSMProfile(ctx, p.ID, "m1"); err != nil {
				t.Fatalf("deleting a deleted profile: %v", err)
			}
		})
	}
}

// A host cannot be created on, or moved to, a profile its member does not have.
func TestSSMHostWritesNeedTheMembersProfile(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mine := SSMProfile{ID: NewID(), MembershipID: "m1", Label: "mine", CreatedAt: NowTS()}
			theirs := SSMProfile{ID: NewID(), MembershipID: "m2", Label: "theirs", CreatedAt: NowTS()}
			for _, x := range []SSMProfile{mine, theirs} {
				if err := st.CreateSSMProfile(ctx, x); err != nil {
					t.Fatal(err)
				}
			}
			h := SSMHost{ID: NewID(), MembershipID: "m1", Alias: "web", InstanceID: "i-1", CreatedAt: NowTS()}
			for _, pid := range []string{"", "nope", theirs.ID} {
				h.ProfileID = pid
				if err := st.CreateSSMHost(ctx, h); !errors.Is(err, ErrSSMProfileNotFound) {
					t.Fatalf("create on profile %q: got %v, want ErrSSMProfileNotFound", pid, err)
				}
			}
			if rows, _ := st.ListSSMHosts(ctx, "m1"); len(rows) != 0 {
				t.Fatalf("refused creates left %d host(s)", len(rows))
			}
			h.ProfileID = mine.ID
			if err := st.CreateSSMHost(ctx, h); err != nil {
				t.Fatal(err)
			}
			moved := h
			moved.ProfileID, moved.Alias = theirs.ID, "renamed"
			if err := st.UpdateSSMHost(ctx, moved); !errors.Is(err, ErrSSMProfileNotFound) {
				t.Fatalf("update onto another member's profile: got %v", err)
			}
			got, _, _ := st.GetSSMHost(ctx, h.ID)
			if got.ProfileID != mine.ID || got.Alias != "web" {
				t.Fatalf("refused update changed the host: %+v", got)
			}
		})
	}
}

// The delete's "no host uses it" and a host write's "the profile exists" are decided under
// one row lock. Measured on Postgres without lockSSMProfile: both reads pass inside
// concurrent transactions and hosts are left on the deleted profile.
func TestSSMProfileDeleteRacesHostWrites(t *testing.T) {
	for name, st := range ssmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			stranded := 0
			for round := 0; round < 40; round++ {
				p := SSMProfile{ID: NewID(), MembershipID: "m1", Label: "p", CreatedAt: NowTS()}
				if err := st.CreateSSMProfile(ctx, p); err != nil {
					t.Fatal(err)
				}
				var wg sync.WaitGroup
				start := make(chan struct{})
				for i := 0; i < 4; i++ {
					wg.Add(2)
					go func(i int) {
						defer wg.Done()
						<-start
						h := SSMHost{ID: NewID(), MembershipID: "m1", Alias: fmt.Sprintf("h%d-%d", round, i),
							ProfileID: p.ID, InstanceID: "i-1", CreatedAt: NowTS()}
						if err := st.CreateSSMHost(ctx, h); err != nil && !errors.Is(err, ErrSSMProfileNotFound) {
							t.Errorf("create host: %v", err)
						}
					}(i)
					go func() {
						defer wg.Done()
						<-start
						var inUse *SSMProfileInUseError
						if err := st.DeleteSSMProfile(ctx, p.ID, "m1"); err != nil && !errors.As(err, &inUse) {
							t.Errorf("delete profile: %v", err)
						}
					}()
				}
				close(start)
				wg.Wait()
				if _, found, _ := st.GetSSMProfile(ctx, p.ID); found {
					continue
				}
				hosts, err := st.ListSSMHosts(ctx, "m1")
				if err != nil {
					t.Fatal(err)
				}
				for _, h := range hosts {
					if h.ProfileID == p.ID {
						stranded++
					}
				}
			}
			if stranded > 0 {
				t.Fatalf("%d host(s) left on a deleted profile", stranded)
			}
		})
	}
}

func aliases(hs []SSMHost) string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.Alias)
	}
	return strings.Join(out, ",")
}
