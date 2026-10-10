package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// runHomeDEKStatus is `af-cp home-dek-status`: a read-only count of how far the homes are on
// their own credential-store key (AF_WORKSPACE_DEK=random, dek.go). It needs no custodian and
// opens nothing; exit 2 when the store cannot be read.
func runHomeDEKStatus(args []string) {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: af-cp home-dek-status")
		os.Exit(2)
	}
	st, where, err := openRewrapStore()
	if err != nil {
		log.Printf("home-dek-status: open the metadata store: %v", err)
		os.Exit(2)
	}
	defer st.Close()
	log.Printf("home-dek-status: %s", where)
	if err := printHomeDEKStatus(context.Background(), st, os.Stdout); err != nil {
		log.Printf("home-dek-status: %v", err)
		os.Exit(2)
	}
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
