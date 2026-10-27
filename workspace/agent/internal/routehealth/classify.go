// Package routehealth is the failure classifier and the cooldown table behind the routing of
// Agent Fleet's own LLM calls (assistant chat, one-shots, memory review; #1564). It decides
// nothing about which backend runs: the resolvers in chatx ask it whether a (kind, model) is
// parked, and report every failure and success back to it.
//
// CLI sessions are out of scope: they stay on their model and book a resume at the reset
// (sessionx/rate_limit_resume.go), because switching mid-session loses the context.
package routehealth

import (
	"regexp"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Class is what a failed call says about the backend's availability.
type Class string

const (
	// ClassWindow is a subscription usage window (5-hour, weekly, per-model). Waiting clears
	// it; the reset time is used when the text names one.
	ClassWindow Class = "window"
	// ClassSpend is a spend or credit cap. Waiting never clears it, so it parks for the
	// longest backoff step and is re-probed after that.
	ClassSpend Class = "spend"
	// ClassRateLimit is a temporary request-rate 429 that is not a usage limit.
	ClassRateLimit Class = "rate_limit"
	// ClassServer is an overload or 5xx on the provider's side.
	ClassServer Class = "server"
	// ClassConfig is a request that can never succeed as configured: an unknown model, a
	// 404, an invalid request. It is a defect to surface, never a cooldown: parking on it
	// would turn a deterministic bug into a flapping availability signal.
	ClassConfig Class = "config"
	// ClassAuth is a lost or expired login. The resolvers already skip a kind that is not
	// logged in (chatx headlessAgentAvailable), so it is not parked here either.
	ClassAuth Class = "auth"
	// ClassOther is any failure the text does not explain. It is not parked: an unknown
	// verdict that parked would let one odd error take a backend out for minutes.
	ClassOther Class = "other"
)

// Scope is how much of the kind a verdict parks.
type Scope string

const (
	// ScopeModel parks one (kind, model) and leaves the kind's other models usable: a
	// request-rate quota is per model at the major providers.
	ScopeModel Scope = "model"
	// ScopeKind parks every model of the kind. Subscription windows and spend caps are
	// account-wide, so another model on the same login hits the same wall.
	ScopeKind Scope = "kind"
)

// Verdict is the classification of one failed call.
type Verdict struct {
	Class Class
	Scope Scope
	// ResetAt is when the provider said the limit lifts; zero when it named no time.
	ResetAt time.Time
}

// Cools reports whether the verdict parks anything at all.
func (v Verdict) Cools() bool {
	switch v.Class {
	case ClassWindow, ClassSpend, ClassRateLimit, ClassServer:
		return true
	}
	return false
}

// The generic markers below are applied to every kind after the kind's own measured reader
// (claude, muse) has had its say. They are lower-cased substrings of the error text.
var (
	// configMarkers name a request that is wrong as configured.
	configMarkers = []string{
		"model_not_found",
		"model not found",
		"unknown model",
		"invalid model",
		"model is not supported",
		"not a valid model",
		"does not exist",
		"prompt is too long", // claude -p (chat_recover.go handles it by compacting)
	}
	authMarkers = []string{
		"unauthorized",
		"authentication",
		"re-authenticate",
		"run /login",
		"not logged in",
		"invalid api key",
		"refresh your login",
	}
	spendMarkers = []string{
		"spend limit",
		"credit balance",
		"insufficient credit",
		"insufficient_quota",
		"insufficient balance",        // opencode corpus (agents/opencode/retry_test.go)
		"exceeded your current quota", // "… please check your plan and billing details."
		"billing details",
	}
	// windowMarkers name a subscription usage window. A bare "quota" is not one: per-minute
	// request quotas and disk quotas say "quota exceeded" too (see quotaClass).
	windowMarkers = []string{
		"usage limit",
		"session limit",
		"weekly limit",
		"subscription quota",
		"usage quota",
		"usagelimitexceeded",
	}
	rateLimitMarkers = []string{
		"rate limit",
		"rate_limit",
		"too many requests",
		"temporarily limiting requests",
	}
	serverMarkers = []string{
		"overloaded",
		"server error",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"internal error",
	}
)

// statusRe reads an HTTP status only where the text says it is one: right after HTTP, status,
// statusCode, code, error, returned or "failed with", or as the first word of the message. A
// bare whole-word number is not enough: "max_tokens must be greater than 512" names no status,
// and in "request_id=req-401-abc: HTTP 503" the 401 is part of an id.
var statusRe = regexp.MustCompile(`(?i)(?:^\s*|\b(?:http(?:/[\d.]+)?|status(?:[ _-]?code)?|code|error|returned|failed with)\W{0,3})([45]\d\d)\b`)

// Classify reads one failed call's error text. kind selects the measured reader for providers
// that have one; now anchors a reset time that the text gives as a wall clock only.
//
// Order matters. The kind's own reader goes first because it knows the provider's exact
// wording, including negations such as claude's "(not your usage limit)". Of the generic
// markers, config and auth come before the availability ones because parking on them never
// helps; spend comes before window because calling a spend cap a window makes the router
// expect a reset that never comes.
func Classify(kind, msg string, now time.Time) Verdict {
	switch kind {
	case session.KindClaude:
		if lk, at, ok := claude.LimitInMessage(msg, now); ok {
			if lk == claude.LimitSpend {
				return Verdict{Class: ClassSpend, Scope: ScopeKind}
			}
			if claude.IsPerModelLimit(msg) {
				return Verdict{Class: ClassWindow, Scope: ScopeModel, ResetAt: at}
			}
			return Verdict{Class: ClassWindow, Scope: ScopeKind, ResetAt: at}
		}
	case session.KindMuse:
		if at, ok := muse.LimitInMessage(msg); ok {
			return Verdict{Class: ClassWindow, Scope: ScopeKind, ResetAt: at}
		}
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "not your usage limit"):
		return Verdict{Class: ClassRateLimit, Scope: ScopeModel}
	case containsAny(low, configMarkers):
		return Verdict{Class: ClassConfig}
	case containsAny(low, authMarkers):
		return Verdict{Class: ClassAuth}
	case containsAny(low, spendMarkers):
		return Verdict{Class: ClassSpend, Scope: ScopeKind}
	case containsAny(low, windowMarkers):
		return Verdict{Class: ClassWindow, Scope: ScopeKind}
	case containsAny(low, rateLimitMarkers), quotaIsRate(low):
		return Verdict{Class: ClassRateLimit, Scope: ScopeModel}
	case containsAny(low, serverMarkers):
		return Verdict{Class: ClassServer, Scope: ScopeModel}
	}
	for _, m := range statusRe.FindAllStringSubmatch(msg, -1) {
		switch s := m[1]; {
		case s == "401" || s == "403":
			return Verdict{Class: ClassAuth}
		case s == "400" || s == "404":
			return Verdict{Class: ClassConfig}
		case s == "429":
			return Verdict{Class: ClassRateLimit, Scope: ScopeModel}
		case s[0] == '5':
			return Verdict{Class: ClassServer, Scope: ScopeModel}
		}
	}
	return Verdict{Class: ClassOther}
}

// quotaIsRate reports whether a "quota exceeded / exhausted" text that named no subscription
// window (windowMarkers) and no billing (spendMarkers) is read as a request-rate quota: one
// model, a short wait. "Quota exceeded for requests per minute" is the common shape, and
// nothing in a bare quota text says it is account-wide or long. A disk quota is no LLM limit
// at all (the muse reader rejects it too).
func quotaIsRate(low string) bool {
	if strings.Contains(low, "disk quota") {
		return false
	}
	if !strings.Contains(low, "quota exceeded") && !strings.Contains(low, "quota exhausted") {
		return false
	}
	return true
}

func containsAny(s string, subs []string) bool {
	for _, m := range subs {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}
