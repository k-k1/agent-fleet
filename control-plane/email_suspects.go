package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

type storeEmailSuspect = store.EmailSuspect

// runEmailSuspects is `af-cp identity-email-check`: a read-only list of identities that
// hold memberships in more than one tenant and whose address may not have come from the
// person's own login (no sign-in method linked, or an identity.email_* audit entry).
// Exit 0: none; 1: some listed; 2: the store could not be read.
func runEmailSuspects() {
	st, where, err := openRewrapStore()
	if err != nil {
		log.Printf("identity-email-check: open the metadata store: %v", err)
		os.Exit(2)
	}
	defer st.Close()
	log.Printf("identity-email-check: %s", where)
	os.Exit(printEmailSuspects(context.Background(), st, os.Stdout))
}

func printEmailSuspects(ctx context.Context, st interface {
	ListEmailSetSuspects(context.Context) ([]storeEmailSuspect, error)
}, w io.Writer) int {
	list, err := st.ListEmailSetSuspects(ctx)
	if err != nil {
		log.Printf("identity-email-check: %v", err)
		return 2
	}
	for _, e := range list {
		var why []string
		if e.NoLogin {
			why = append(why, "no-sign-in-method")
		}
		if e.Audited {
			why = append(why, "email-audited")
		}
		fmt.Fprintf(w, "%s\t%s\ttenants=%s\t%s\n", e.UserKey, e.Email, strings.Join(e.Tenants, ","), strings.Join(why, ","))
	}
	fmt.Fprintf(w, "%d identities\n", len(list))
	if len(list) > 0 {
		return 1
	}
	return 0
}
