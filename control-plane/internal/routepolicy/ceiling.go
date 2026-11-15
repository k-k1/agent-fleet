package routepolicy

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// EnvCeiling is the operator's ceiling (D9c): the outermost layer, set in the Control Plane's
// environment (on ECS the ModelRouteCeiling parameter), readable from the stack and unchangeable
// from the Console. Empty is no ceiling.
const EnvCeiling = "AF_MODEL_ROUTE_CEILING"

// ParseCeiling reads the ceiling's spelling: key=value pairs separated by ";", list values by
// ",", spaces ignored.
//
//	classes=-subscription;destinations=-opencode.ai;except=copilot-login
//	destinations=aws-bedrock
//
// classes and destinations name either the values allowed ("destinations=aws-bedrock") or, every
// item prefixed with "-", the values removed from all ("classes=-subscription"); a list mixing
// the two forms is refused. except and deny are route ids (D1d). endpoints are the host patterns
// that may be approved as self-hosted endpoints at all (an exact host or "*.suffix"; unset under
// a ceiling that restricts destinations, none may). Anything unparsable, unknown, a per-connection
// route in except or a pattern on a vendor's hosts is an error, never "allow all": the caller
// refuses to start.
func ParseCeiling(s string) (Rule, error) {
	var r Rule
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !ok {
			return Rule{}, fmt.Errorf("%q is not key=value", part)
		}
		if seen[key] {
			return Rule{}, fmt.Errorf("%s is given twice", key)
		}
		seen[key] = true
		var items []string
		for _, v := range strings.Split(val, ",") {
			if v = strings.TrimSpace(v); v != "" {
				items = append(items, v)
			}
		}
		switch key {
		case "classes":
			l, err := setList(key, items, Classes())
			if err != nil {
				return Rule{}, err
			}
			r.Classes = &l
		case "destinations":
			l, err := setList(key, items, Destinations())
			if err != nil {
				return Rule{}, err
			}
			r.Destinations = &l
		case "except":
			r.Except = items
		case "deny":
			r.Deny = items
		case "endpoints":
			l := append([]string{}, items...)
			r.Endpoints = &l
		default:
			return Rule{}, fmt.Errorf("unknown key %q (classes, destinations, except, deny, endpoints)", key)
		}
	}
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	return r.Normalize(), nil
}

// setList turns a list in either form into the allowed values.
func setList(key string, items, known []string) ([]string, error) {
	neg := 0
	for _, v := range items {
		if strings.HasPrefix(v, "-") {
			neg++
		}
	}
	switch {
	case neg == 0:
		return items, nil
	case neg != len(items):
		return nil, fmt.Errorf("%s mixes allowed and removed values; use one form", key)
	}
	out := []string{}
	for _, k := range known {
		// "Every destination but these" never includes unclassified, which no rule can allow.
		if k == Unclassified {
			continue
		}
		if !slices.Contains(items, "-"+k) {
			out = append(out, k)
		}
	}
	for _, v := range items {
		if !slices.Contains(known, strings.TrimPrefix(v, "-")) {
			return nil, fmt.Errorf("unknown %s value %q; known: %s", key, strings.TrimPrefix(v, "-"), strings.Join(known, ", "))
		}
	}
	return out, nil
}

// CeilingFromEnv reads and parses EnvCeiling, returning the raw value with the rule.
func CeilingFromEnv() (raw string, r Rule, err error) {
	raw = strings.TrimSpace(os.Getenv(EnvCeiling))
	r, err = ParseCeiling(raw)
	if err != nil {
		return raw, Rule{}, fmt.Errorf("%s=%q: %w", EnvCeiling, raw, err)
	}
	return raw, r, nil
}
