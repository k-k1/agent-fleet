// Package providedroutes holds the words and the pure rules of ADR 0129's provided routes: Bedrock
// that the tenant or the deployment pays for, beside the member's own (decision 1). It is data
// and validation only; nothing here talks to AWS or decides an admission.
//
// A provided source is bound for life by its payer fingerprint: a session's selection names
// "<source id>#<fingerprint>", and a source edited into another payer refuses the bound session
// instead of moving its bill. What is in the fingerprint is exactly what decides who pays and
// where the calls go (the role ARN, the external id's version, the Region set); the model
// allow-list and the budgets are not, because changing them moves no bill (they have the
// allow-list's policy version instead).
package providedroutes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/routeprice"
)

// Credential sources of a provided route (ADR 0129 decision 1). The member's own source has no
// constant here: it is the Agent's, and never reaches these tables.
const (
	SourceTenant     = "tenant"
	SourceDeployment = "deployment"
)

// Route ids of the provided sources, as the route catalogue and the ledger carry them.
const (
	RouteTenant     = "bedrock-tenant"
	RouteDeployment = "bedrock-deployment"
)

// ValidSource reports whether s is a provided credential source.
func ValidSource(s string) bool { return s == SourceTenant || s == SourceDeployment }

// RouteOf is the catalogue route id of a provided source; "" for anything else.
func RouteOf(source string) string {
	switch source {
	case SourceTenant:
		return RouteTenant
	case SourceDeployment:
		return RouteDeployment
	}
	return ""
}

// SourceOf is the provided source of a catalogue route id; "" for any other route, the member's
// bedrock included.
func SourceOf(route string) string {
	switch route {
	case RouteTenant:
		return SourceTenant
	case RouteDeployment:
		return SourceDeployment
	}
	return ""
}

// Payer is what a source's fingerprint covers: who pays and where the calls go.
type Payer struct {
	RoleARN string
	// ExternalIDVersion is bumped by every rotation of a tenant connection's external id; the
	// deployment source has none and keeps 0.
	ExternalIDVersion int
	Regions           []string
}

// NormalizeRegions trims, sorts and de-duplicates a Region set. Order and repetition say nothing
// about who pays, so they never move a fingerprint.
func NormalizeRegions(regions []string) []string { return normalizeSet(regions) }

func normalizeSet(in []string) []string {
	out := make([]string, 0, len(in))
	for _, r := range in {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Fingerprint is the payer fingerprint: 16 lower-case hex digits, the shape the member route's
// AWS binding already uses (routes.BindAWSConnection on the Agent).
func Fingerprint(p Payer) string {
	h := sha256.New()
	// Length-prefixed fields, so no value can be shifted into its neighbour.
	for _, f := range append([]string{"provided-route-payer/v1", strings.TrimSpace(p.RoleARN), strconv.Itoa(p.ExternalIDVersion)},
		NormalizeRegions(p.Regions)...) {
		fmt.Fprintf(h, "%d:%s\n", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// Bind is the stored form of a session's provided source: "<source id>#<fingerprint>".
func Bind(sourceID, fingerprint string) string { return sourceID + "#" + fingerprint }

// ParseBinding splits a Bind. ok is false for anything that is not one.
func ParseBinding(s string) (sourceID, fingerprint string, ok bool) {
	id, fp, found := strings.Cut(s, "#")
	if !found || !sourceIDRe.MatchString(id) || !fingerprintRe.MatchString(fp) {
		return "", "", false
	}
	return id, fp, true
}

var (
	sourceIDRe    = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	fingerprintRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	// roleARNRe is an IAM role in any partition. The account id is what the probes and the trust
	// policy are about, so it must be there in full.
	roleARNRe = regexp.MustCompile(`^arn:aws(-[a-z]+)*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	regionRe  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
)

// Validate checks a source registration's payer and allow-list before it is stored.
//
// The allow-list names foundation-model and system inference profile ids verbatim, never an ARN
// (D2: an application inference profile in another account must not be reachable by naming it),
// and only models the price-and-limits table has a row for (D4: an unpriced model is refused,
// never priced at zero).
func Validate(source string, p Payer, allow []string) error {
	if !ValidSource(source) {
		return fmt.Errorf("unknown provided source %q", source)
	}
	if !roleARNRe.MatchString(strings.TrimSpace(p.RoleARN)) {
		return fmt.Errorf("role ARN %q is not an IAM role ARN", p.RoleARN)
	}
	if p.ExternalIDVersion < 0 || source == SourceDeployment && p.ExternalIDVersion != 0 {
		return fmt.Errorf("external id version %d is not valid for a %s source", p.ExternalIDVersion, source)
	}
	regions := NormalizeRegions(p.Regions)
	if len(regions) == 0 {
		return fmt.Errorf("a provided source needs at least one Region")
	}
	for _, r := range regions {
		if !regionRe.MatchString(r) {
			return fmt.Errorf("%q is not an AWS Region", r)
		}
	}
	for _, m := range allow {
		if strings.HasPrefix(m, "arn:") {
			return fmt.Errorf("allow-list entry %q is an ARN; name a model or system inference profile id", m)
		}
		if _, ok := routeprice.Lookup(m); !ok {
			return fmt.Errorf("allow-list entry %q has no price and limits row", m)
		}
	}
	return nil
}

// NormalizeAllowList trims, sorts and de-duplicates a model allow-list, so the stored list and
// its policy version change only when the set does.
func NormalizeAllowList(allow []string) []string { return normalizeSet(allow) }

// AdmissionMonth is the budget period a call belongs to: the calendar month in UTC of its
// admission (D4), "2006-01". A settlement goes to this month whenever it arrives.
func AdmissionMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// Reservation states (D4). A row is written in_flight before anything is sent upstream and moves
// to settled exactly once.
const (
	StateInFlight = "in_flight"
	StateSettled  = "settled"
)
