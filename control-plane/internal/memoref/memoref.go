// Package memoref validates the typed reference a queue message may carry (ADR 0137 section 4):
// a pointer {provider, key, url} to a work item, never a copy of it. It knows nothing of the
// store or the wire; the memo API owns where a reference is kept and how it is answered.
package memoref

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Bounds on a reference. The key and provider are held to what the Agent keeps of a launch's work
// item (sessionx cleanWorkItemRef clips the key at 200 and the provider at 32), so what a fire
// sends is what the session records.
const (
	KeyMax      = 200
	ProviderMax = 32
	URLMax      = 2048
)

// Ref is a reference as it is stored and sent.
type Ref struct {
	Provider, Key, URL string
}

// Empty says no part is set.
func (r Ref) Empty() bool { return r.Provider == "" && r.Key == "" && r.URL == "" }

var (
	// owner/repo#N, the key GitHub and Bitbucket items carry. A bare #N has no repository to
	// read it from and is refused.
	slugKey = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#[1-9][0-9]*$`)
	// PROJ-12: a Jira project key is upper case and starts with a letter.
	jiraKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)
)

// Error is a refusal, worded for the member.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func refuse(format string, a ...any) error { return &Error{fmt.Sprintf(format, a...)} }

// Normalize checks a reference and returns it in the form it is stored and compared in: the
// provider and a GitHub or Bitbucket key in lower case and a Jira key in upper case, as the Agent keys a launch's work item.
// All three parts are set or none is (the zero Ref is no reference). known says which providers
// the work items know.
func Normalize(in Ref, known func(string) bool) (Ref, error) {
	if in.Empty() {
		return Ref{}, nil
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	key := strings.TrimSpace(in.Key)
	u := strings.TrimSpace(in.URL)
	if provider == "" || key == "" || u == "" {
		return Ref{}, refuse("a reference needs provider, key and url together")
	}
	if len(provider) > ProviderMax || !known(provider) {
		return Ref{}, refuse("the reference's provider must be one of github, jira, bitbucket")
	}
	if len(key) > KeyMax {
		return Ref{}, refuse("the reference's key is over %d bytes", KeyMax)
	}
	if provider == "jira" {
		key = strings.ToUpper(key)
		if !jiraKey.MatchString(key) {
			return Ref{}, refuse("a Jira key looks like PROJ-12")
		}
	} else {
		if !slugKey.MatchString(key) {
			return Ref{}, refuse("a %s key looks like owner/repo#12", provider)
		}
		// A repository name is not case-sensitive: one stored form keeps the same item one key.
		key = strings.ToLower(key)
	}
	if len(u) > URLMax {
		return Ref{}, refuse("the reference's url is over %d bytes", URLMax)
	}
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" || strings.ContainsAny(u, " \t\r\n\x00") {
		return Ref{}, refuse("the reference's url must be an http or https address")
	}
	return Ref{Provider: provider, Key: key, URL: u}, nil
}

// Ident is what two messages of one queue are compared by: the provider and the key, with the
// case of the key folded (a GitHub or Bitbucket repository name is not case-sensitive). "" for no
// reference.
func Ident(provider, key string) string {
	if key == "" {
		return ""
	}
	return provider + "\x00" + strings.ToLower(key)
}
