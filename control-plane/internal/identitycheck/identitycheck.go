// Package identitycheck is the report behind `control-plane identity-email-check`: a
// read-only listing of identities held by more than one tenant whose address may not have
// come from the person's own login.
package identitycheck

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Lister is the slice of the store the report reads.
type Lister interface {
	ListEmailSetSuspects(context.Context) ([]store.EmailSuspect, error)
}

// Print writes one line per suspect and a count. Exit code: 0 none listed, 1 some listed,
// 2 the store could not be read.
func Print(ctx context.Context, st Lister, w io.Writer) int {
	list, err := st.ListEmailSetSuspects(ctx)
	if err != nil {
		fmt.Fprintf(w, "identity-email-check: %v\n", err)
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
