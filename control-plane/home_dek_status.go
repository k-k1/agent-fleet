package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// runHomeDEKStatus is `af-cp home-dek-status [--remigrate <membership-id>]`: a read-only count of
// how far the homes are on their own credential-store key (AF_WORKSPACE_DEK=random, dek.go).
// --remigrate puts one confirmed home back to 'migrating', so its next start hands out the
// derived key beside the home's key again: the way back for a home restored from a copy made
// before its store moved, which the home's key alone does not open. It needs no custodian and
// opens nothing; exit 2 on usage or a store that cannot be read, 1 when --remigrate found no
// confirmed home by that id.
func runHomeDEKStatus(args []string) {
	fs := flag.NewFlagSet("home-dek-status", flag.ContinueOnError)
	remigrate := fs.String("remigrate", "", "membership id of a confirmed home to put back to migrating")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: af-cp home-dek-status [--remigrate <membership-id>]")
		os.Exit(2)
	}
	st, where, err := openRewrapStore()
	if err != nil {
		log.Printf("home-dek-status: open the metadata store: %v", err)
		os.Exit(2)
	}
	defer st.Close()
	log.Printf("home-dek-status: %s", where)
	ctx := context.Background()
	if *remigrate != "" {
		os.Exit(remigrateHomeDEK(ctx, st, *remigrate, os.Stdout))
	}
	if err := printHomeDEKStatus(ctx, st, os.Stdout); err != nil {
		log.Printf("home-dek-status: %v", err)
		os.Exit(2)
	}
}

func remigrateHomeDEK(ctx context.Context, st *store.SQL, membershipID string, w io.Writer) int {
	ok, err := st.RemigrateHomeDEK(ctx, membershipID)
	switch {
	case err != nil:
		log.Printf("home-dek-status: remigrate: %v", err)
		return 2
	case !ok:
		fmt.Fprintf(w, "no confirmed home key for membership %s; nothing changed\n", membershipID)
		return 1
	}
	fmt.Fprintf(w, "membership %s is migrating again; restart its workspace to re-seal the store\n", membershipID)
	return 0
}

func printHomeDEKStatus(ctx context.Context, st *store.SQL, w io.Writer) error {
	c, err := st.CountHomeDEKs(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "homes with their own key, migrating: %d\n", c.Migrating)
	fmt.Fprintf(w, "homes with their own key, confirmed: %d\n", c.Random)
	fmt.Fprintf(w, "workspaces whose home has no key of its own yet: %d\n", c.WithoutKey)
	return nil
}
