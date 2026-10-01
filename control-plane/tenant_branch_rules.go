package main

// Tenant branch naming rules — the tenant layer of ADR 0103 (decision 10).
//
//	tenant_admin ──▶ GET/PUT /api/admin/tenants/{slug}/branch-rules
//	                     │  tenant_branch_rules (one JSON list per tenant)
//	member's Agent ──(AF_BRANCH_RULES_TOKEN)──▶ GET /internal/branch-rules   (every 5 minutes)
//
// The Agent resolves names; the CP only keeps the list and hands it out. A rule is
// `{match, name, base, types}` in the Agent's own shape (workspace/agent/internal/branchrule),
// and there is no enforce flag: rules warn and never refuse (decision 8).
//
// The checks here are the Agent's (decision 3: tenant rules go through the same key and
// ref-name checks), run with the same `git check-ref-format`. The Agent checks again on
// receipt and drops what it refuses, so a disagreement costs a rule, never the whole list.
// The shared case table in workspace/agent/internal/branchrule/testdata/rule_checks.json
// keeps the two in step.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// branchKinds is the Agent's closed kind vocabulary (branchrule.Kinds).
var branchKinds = []string{"feature", "bugfix", "hotfix", "release", "support", "docs", "chore", "refactor"}

// Bounds on what an admin can store. The Agent renders every rule on each resolve and runs
// git per value when it checks them, so the list is kept to what a person writes by hand.
const (
	branchRulesMaxBody  = 64 << 10
	branchRulesMaxRules = 100
	branchRulesMaxFrom  = 50
	branchRulesMaxText  = 256
)

// branchKindWire is one kind's overrides. A nil pointer is "not set", which differs from an
// empty prefix (git-flow may declare a kind with no prefix).
type branchKindWire struct {
	Prefix *string  `json:"prefix,omitempty"`
	Base   *string  `json:"base,omitempty"`
	From   []string `json:"from,omitempty"`
}

// branchRuleWire is one rule, in the Agent's JSON shape.
type branchRuleWire struct {
	Match string                    `json:"match"`
	Name  *string                   `json:"name,omitempty"`
	Base  *string                   `json:"base,omitempty"`
	Types map[string]branchKindWire `json:"types,omitempty"`
}

// tenantBranchRulesWire is the admin GET/PUT response, read by the Console's tenant editor
// (console/src/features/settings/tenant/tenantBranchRules.tsx).
type tenantBranchRulesWire struct {
	Tenant    string           `json:"tenant"`
	Rules     []branchRuleWire `json:"rules"`
	UpdatedBy string           `json:"updated_by,omitempty"`
	UpdatedAt string           `json:"updated_at,omitempty"`
}

// branchRulesBridgeWire is GET /internal/branch-rules. Rules is always a list: the Agent
// reads a missing list as a broken answer and keeps its copy, and an empty one as "the
// tenant has no rules".
type branchRulesBridgeWire struct {
	Rules []branchRuleWire `json:"rules"`
}

func validBranchKind(k string) bool {
	for _, v := range branchKinds {
		if v == k {
			return true
		}
	}
	return false
}

// validBranchMatch is branchrule.ValidMatch: a bare `*` or `/`-separated non-empty
// segments, each literal or `*`.
func validBranchMatch(p string) bool {
	if p == "*" {
		return true
	}
	if p == "" {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || (strings.Contains(s, "*") && s != "*") {
			return false
		}
	}
	return true
}

// validBranchName is branchrule.ValidBranch: "-" would reach git as an option and `@{-N}` is
// expanded by --branch, so both are refused before git sees them.
func validBranchName(v string) bool {
	if v == "" || strings.HasPrefix(v, "-") || strings.Contains(v, "@{") {
		return false
	}
	cmd := exec.Command("git", "check-ref-format", "--branch", v)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func validBranchBase(v string) bool { return v == "head" || v == "default" || validBranchName(v) }

// validBranchPrefix checks `<prefix>x`: a prefix may be empty or end in "/", which
// check-ref-format rejects on its own.
func validBranchPrefix(v string) bool { return v == "" || validBranchName(v+"x") }

func branchQuote(s string) string { return strconv.Quote(s) }

// validateTenantBranchRules is branchrule.ValidateRules(rules, false) plus the size bounds,
// and trims match the way the Agent compares it. A tenant `*` rule may set name: it is the
// tenant's default template, below the user's own.
func validateTenantBranchRules(rules []branchRuleWire) error {
	if len(rules) > branchRulesMaxRules {
		return fmt.Errorf("at most %d rules", branchRulesMaxRules)
	}
	for i := range rules {
		r := &rules[i]
		n := i + 1
		r.Match = strings.TrimSpace(r.Match)
		if !validBranchMatch(r.Match) {
			return fmt.Errorf("rule %d: match %s is not a host/owner/repo pattern", n, branchQuote(r.Match))
		}
		if r.Name != nil && len(*r.Name) > branchRulesMaxText {
			return fmt.Errorf("rule %d: name is longer than %d bytes", n, branchRulesMaxText)
		}
		if r.Base != nil && !validBranchBase(*r.Base) {
			return fmt.Errorf("rule %d: base %s is not a branch name", n, branchQuote(*r.Base))
		}
		for k, kr := range r.Types {
			if !validBranchKind(k) {
				return fmt.Errorf("rule %d: kind %s is not in the vocabulary", n, branchQuote(k))
			}
			if kr.Prefix != nil && !validBranchPrefix(*kr.Prefix) {
				return fmt.Errorf("rule %d: %s prefix %s is not a branch prefix", n, k, branchQuote(*kr.Prefix))
			}
			if kr.Base != nil && !validBranchBase(*kr.Base) {
				return fmt.Errorf("rule %d: %s base %s is not a branch name", n, k, branchQuote(*kr.Base))
			}
			if len(kr.From) > branchRulesMaxFrom {
				return fmt.Errorf("rule %d: %s lists more than %d types and labels", n, k, branchRulesMaxFrom)
			}
			for _, f := range kr.From {
				if strings.TrimSpace(f) == "" || len(f) > branchRulesMaxText {
					return fmt.Errorf("rule %d: %s has an empty or too long type or label", n, k)
				}
			}
		}
	}
	return nil
}

// loadTenantBranchRules reads the stored list; no row is an empty list.
func loadTenantBranchRules(r *http.Request, st store.Store, tenantID string) (store.TenantBranchRules, []branchRuleWire, error) {
	row, ok, err := st.GetTenantBranchRules(r.Context(), tenantID)
	if err != nil || !ok {
		return row, []branchRuleWire{}, err
	}
	rules := []branchRuleWire{}
	if err := json.Unmarshal([]byte(row.Rules), &rules); err != nil {
		return row, nil, fmt.Errorf("stored branch rules are not JSON: %w", err)
	}
	return row, rules, nil
}

// tenantBranchRules (GET /api/admin/tenants/{slug}/branch-rules).
func (a adminAPI) tenantBranchRules(w http.ResponseWriter, r *http.Request) {
	_, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	row, rules, err := loadTenantBranchRules(r, a.mgr.store, t.ID)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, tenantBranchRulesWire{Tenant: t.Slug, Rules: rules, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
}

// setTenantBranchRules (PUT /api/admin/tenants/{slug}/branch-rules) replaces the list. A rule
// that fails a check is refused here with the reason; the names a rule produces are only
// ever warned about, in the Agent.
func (a adminAPI) setTenantBranchRules(w http.ResponseWriter, r *http.Request) {
	ident, t, ok := a.tenantAdminFor(w, r, r.PathValue("slug"))
	if !ok {
		return
	}
	rules, aerr := decodeTenantBranchRules(w, r)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if err := validateTenantBranchRules(rules); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "invalid_rule", err.Error()})
		return
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(rules); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	row := store.TenantBranchRules{TenantID: t.ID, Rules: strings.TrimSpace(buf.String()), UpdatedBy: ident.ID, UpdatedAt: store.NowTS()}
	if err := a.mgr.store.PutTenantBranchRules(r.Context(), row); err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	_ = a.mgr.store.InsertAudit(r.Context(), store.AuditLog{
		ID: store.NewID(), TenantID: t.ID, ActorKind: "user", ActorID: ident.ID,
		Action: "tenant.branch_rules", Target: t.Slug,
		Detail: "rules=" + strconv.Itoa(len(rules)), At: row.UpdatedAt,
	})
	writeJSON(w, http.StatusOK, tenantBranchRulesWire{Tenant: t.Slug, Rules: rules, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt})
}

// decodeTenantBranchRules reads a PUT body. The save replaces the whole list, so anything
// short of one complete object with a `rules` array is refused rather than read as "no rules":
// a missing or null list, trailing data or a body past the limit would otherwise empty the
// tenant's rules on a client bug. Removing every rule takes an explicit `"rules": []`.
// Decode errors are invalid_rule too, so the editor shows git's or the decoder's own words
// (`unknown field "prefixes"`) rather than a generic "malformed request".
func decodeTenantBranchRules(w http.ResponseWriter, r *http.Request) ([]branchRuleWire, *apiError) {
	var body struct {
		Rules *[]branchRuleWire `json:"rules"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, branchRulesMaxBody))
	// A misspelt key ("prefixes", "kinds") would otherwise be dropped silently and the
	// admin would believe the rule says something it does not.
	dec.DisallowUnknownFields()
	err := dec.Decode(&body)
	if err == nil {
		// The limit and the "one object" rule hold to the end of the body.
		if extra := dec.Decode(&struct{}{}); !errors.Is(extra, io.EOF) {
			err = extra
			if err == nil {
				err = errors.New("unexpected data after the rules object")
			}
		}
	}
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		return nil, &apiError{http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("the rules are at most %d bytes", branchRulesMaxBody)}
	case err != nil:
		return nil, &apiError{http.StatusBadRequest, "invalid_rule", "invalid branch rules: " + err.Error()}
	case body.Rules == nil:
		return nil, &apiError{http.StatusBadRequest, "invalid_rule", `"rules" must be a list; send [] to remove every rule`}
	}
	return *body.Rules, nil
}

func branchRulesSignKey(master32 []byte) []byte {
	mac := hmac.New(sha256.New, master32)
	mac.Write([]byte("af-branch-rules-token-sign/v1"))
	return mac.Sum(nil)
}

// mintBranchRulesToken returns the deterministic bridge token for a membership. Format:
// "afb_" + b64url(membershipID) + "." + tag, like the other bridge tokens. Its own
// credential: a leak reads this tenant's naming rules and grants nothing else.
func mintBranchRulesToken(signKey []byte, membershipID string) string {
	return "afb_" + base64.RawURLEncoding.EncodeToString([]byte(membershipID)) + "." + branchRulesTokenTag(signKey, membershipID)
}

func branchRulesTokenTag(signKey []byte, membershipID string) string {
	mac := hmac.New(sha256.New, signKey)
	mac.Write([]byte(membershipID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// verifyBranchRulesToken checks the tag and returns the membership id. The membership itself
// is looked up live by the caller, so a revoked one stops receiving rules on the next poll.
func verifyBranchRulesToken(signKey []byte, token string) (string, bool) {
	body, hasPrefix := strings.CutPrefix(strings.TrimSpace(token), "afb_")
	if !hasPrefix {
		return "", false
	}
	dot := strings.LastIndexByte(body, '.')
	if dot < 0 {
		return "", false
	}
	idRaw, err := base64.RawURLEncoding.DecodeString(body[:dot])
	if err != nil || len(idRaw) == 0 {
		return "", false
	}
	mid := string(idRaw)
	if !hmac.Equal([]byte(body[dot+1:]), []byte(branchRulesTokenTag(signKey, mid))) {
		return "", false
	}
	return mid, true
}

type branchRulesBridgeAPI struct{ mgr *manager }

// list (GET /internal/branch-rules) returns the caller's tenant's rules. The tenant comes
// from the token's membership, never from the request.
func (a branchRulesBridgeAPI) list(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	mid, ok := verifyBranchRulesToken(branchRulesSignKey(a.mgr.tokenSignMaster()), tok)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "invalid branch rules token"})
		return
	}
	mv, ok, err := a.mgr.store.GetMembershipByID(r.Context(), mid)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !ok {
		writeAPIErr(w, &apiError{http.StatusUnauthorized, "unauthenticated", "membership not active"})
		return
	}
	_, rules, err := loadTenantBranchRules(r, a.mgr.store, mv.TenantID)
	if err != nil {
		// An error, not an empty list: the Agent keeps its copy on an error and would drop it
		// on an empty answer.
		writeAPIErr(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, branchRulesBridgeWire{Rules: rules})
}
