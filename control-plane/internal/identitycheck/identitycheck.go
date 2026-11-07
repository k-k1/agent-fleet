// Package identitycheck is the report behind `control-plane identity-email-check`: a
// read-only listing of every identity with active memberships in more than one tenant,
// each with the flags that make its address worth a look.
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

// Print writes one line per identity (user key, email, tenants, flags) and a count.
// derive is the server's address-to-key rule (sanitizeUser): an identity whose key is
// neither derive(email) nor derive(email) plus the collision suffix gets
// key_not_derived_from_email. Exit code: 0 when no identity carries a flag, 1 when some
// does, 2 when the store could not be read.
func Print(ctx context.Context, st Lister, w io.Writer, derive func(string) string) int {
	list, err := st.ListEmailSetSuspects(ctx)
	if err != nil {
		fmt.Fprintf(w, "identity-email-check: %v\n", err)
		return 2
	}
	flagged := 0
	for _, e := range list {
		var flags []string
		if e.NoLogin {
			flags = append(flags, "no_login")
		}
		if e.Audited {
			flags = append(flags, "email_audited")
		}
		if e.Email != "" && !keyFollowsEmail(e.UserKey, e.Email, derive) {
			flags = append(flags, "key_not_derived_from_email")
		}
		if len(flags) > 0 {
			flagged++
		}
		fmt.Fprintf(w, "%s\t%s\ttenants=%s\t%s\n", e.UserKey, e.Email, strings.Join(e.Tenants, ","), strings.Join(flags, ","))
	}
	fmt.Fprintf(w, "%d identities, %d flagged\n", len(list), flagged)
	if flagged > 0 {
		return 1
	}
	return 0
}

func keyFollowsEmail(key, email string, derive func(string) string) bool {
	want := derive(email)
	if key == want {
		return true
	}
	prefix, ok := store.SplitDisambiguatedUserKey(key)
	return ok && prefix == want
}
