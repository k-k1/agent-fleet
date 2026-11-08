package routes

import (
	"fmt"
	"strings"
)

// BedrockModel is a Bedrock model id as a session or an allow-list names it: a foundation-model id
// ("anthropic.claude-opus-4-6-v1") or a system inference profile id, which is the same id behind a
// geographic or global scope ("us.anthropic.…", "global.openai.…"). ARNs are never accepted (ADR
// 0129 decision 1): an ARN can name an application inference profile in another account, so a
// list of ids could be bypassed by naming one, and the model id the gateway sees in the call path
// would no longer say which model runs.
type BedrockModel struct {
	ID string
	// Scope is the inference profile's prefix ("us", "eu", "apac", "jp", "au", "us-gov",
	// "global"), "" for a foundation-model id.
	Scope string
	// Provider is the model vendor segment ("anthropic", "openai", …).
	Provider string
}

// BedrockScopes are the system inference profile prefixes accepted in front of a model id; the
// list is Claude Code 2.1.319's own (the prefixes it maps and passes through).
var BedrockScopes = []string{"us", "eu", "apac", "jp", "au", "us-gov", "global"}

// ParseBedrockModelID checks the form of id only; whether the account and Region serve it is
// learnt from Bedrock's answer, because a route's role carries bedrock:InvokeModel* alone and
// cannot list inference profiles.
func ParseBedrockModelID(id string) (BedrockModel, error) {
	if id != strings.TrimSpace(id) || id == "" {
		return BedrockModel{}, fmt.Errorf("a Bedrock model id is required (for example us.anthropic.claude-opus-4-6-v1)")
	}
	if len(id) > 200 {
		return BedrockModel{}, fmt.Errorf("%.40q… is too long for a Bedrock model id", id)
	}
	if strings.HasPrefix(strings.ToLower(id), "arn:") || strings.Contains(id, "/") {
		return BedrockModel{}, fmt.Errorf("%q is an ARN or a path; name a foundation-model id or a system inference profile id instead (ARNs are refused)", id)
	}
	// The version suffix (":0", or ":0:200k" for a context variant) is the only place a colon goes.
	base, ver, hasVer := strings.Cut(id, ":")
	if hasVer && !bedrockVersion(ver) {
		return BedrockModel{}, fmt.Errorf("%q has a version suffix Bedrock does not use", id)
	}
	parts := strings.Split(base, ".")
	m := BedrockModel{ID: id}
	if len(parts) >= 3 && isScope(parts[0]) {
		m.Scope, parts = parts[0], parts[1:]
	}
	if len(parts) < 2 || !bedrockSegment(parts[0], false) || isScope(parts[0]) {
		return BedrockModel{}, fmt.Errorf("%q is not a Bedrock model id; it reads <provider>.<model>, optionally behind an inference profile scope (%s)", id, strings.Join(BedrockScopes, ", "))
	}
	m.Provider = parts[0]
	for _, p := range parts[1:] {
		if !bedrockSegment(p, true) {
			return BedrockModel{}, fmt.Errorf("%q is not a Bedrock model id", id)
		}
	}
	return m, nil
}

// ParseBedrockAnthropicModelID is ParseBedrockModelID for a harness that only speaks Anthropic's
// API on Bedrock (Claude Code): the provider has to be anthropic.
func ParseBedrockAnthropicModelID(id string) (BedrockModel, error) {
	m, err := ParseBedrockModelID(id)
	if err != nil {
		return m, err
	}
	if m.Provider != "anthropic" {
		return BedrockModel{}, fmt.Errorf("%q is not an Anthropic model on Bedrock (anthropic.… or <scope>.anthropic.…)", id)
	}
	return m, nil
}

func isScope(s string) bool {
	for _, p := range BedrockScopes {
		if s == p {
			return true
		}
	}
	return false
}

// bedrockSegment is one dot-separated part: lower-case provider names, and model names that may
// also carry digits, '-' and '_' (never brackets: "[1m]" is a Claude Code suffix, not an id).
func bedrockSegment(s string, model bool) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && c == '-'
		if model {
			ok = ok || i > 0 && c == '_'
		}
		if !ok {
			return false
		}
	}
	return true
}

// bedrockVersion is what follows the first colon: digits, optionally ":<digits>k" or "m".
func bedrockVersion(v string) bool {
	num, ctx, hasCtx := strings.Cut(v, ":")
	if !digits(num) {
		return false
	}
	if !hasCtx {
		return true
	}
	if len(ctx) < 2 || (ctx[len(ctx)-1] != 'k' && ctx[len(ctx)-1] != 'm') {
		return false
	}
	return digits(ctx[:len(ctx)-1])
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
