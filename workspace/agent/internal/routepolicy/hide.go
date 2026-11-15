package routepolicy

import "slices"

// Hiding what the policy forbids (ADR 0138 D10a): a harness or route the effective policy
// forbids is not listed, at every layer. The listing endpoints narrow what they answer with the
// held policy; the enforcement points refuse what is asked anyway.

// Active is the store the listing endpoints hide by. A seam for tests.
var Active = Shared

// Hiding returns the held policy when it restricts anything; ok is false while nothing is held or
// nothing is restricted, and every list is then answered unchanged.
func Hiding() (p Policy, ok bool) {
	st := Active().State()
	return st.Policy, st.Held && st.Policy.Restricted
}

// Filter is routes narrowed to the ones p allows, in their order; nil when none is.
func (p Policy) Filter(routes []string) []string {
	var out []string
	for _, r := range routes {
		if p.Allows(r) {
			out = append(out, r)
		}
	}
	return out
}

// HidesKind reports whether a harness whose routes (selectable and fixed) are routes is hidden
// under p: it has some and the policy allows none. A kind with no route at all runs no model and
// is never hidden.
func (p Policy) HidesKind(routes []string) bool {
	return len(routes) > 0 && !slices.ContainsFunc(routes, p.Allows)
}
