package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Launch is what a session ran on when it was launched (ADR 0138 D11): the route its create
// resolved to, explicit or not, and the binding of the credential instance behind it (D5b). It
// is recorded once, at create, and never re-resolved from today's credential: a policy judges an
// existing session on this record, and a resume whose current credential no longer matches it is
// a replacement, refused rather than moved. A session created before the record existed has
// none (nil); an empty Route is a create whose route could not be resolved.
type Launch struct {
	Route   string `json:"route,omitempty"`
	Binding string `json:"binding,omitempty"`
}

// Clone returns an independent copy, nil for nil.
func (l *Launch) Clone() *Launch {
	if l == nil {
		return nil
	}
	c := *l
	return &c
}

// Fingerprint identifies a secret or an identity without revealing it: the first 16 hex digits
// of its sha256. It is the same digest claude's anthropic-api binding uses.
func Fingerprint(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

// KeyBinding is the binding of an API key held in the slot named slot (an environment variable
// name, or a CLI's own key slot such as "codex-api-key"): "<slot>@<fingerprint of the key>". A
// new key in the same slot is a new binding; the slot name keeps two copies of one key apart
// (codex's own copy and the shared OPENAI_API_KEY are both openai-api, ADR 0138 D2). "" when the
// key is empty.
func KeyBinding(slot, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return slot + "@" + Fingerprint(key)
}

// AccountBinding is the binding of a login whose credential file names its account: "<login>@<
// fingerprint of the account id>", login naming the login (normally its route id). A token
// refresh keeps the account and so the binding; a sign-in to another account changes it. The
// account id itself (often an e-mail address) never leaves this function. "" when the id is
// empty: the login carries nothing that identifies the account, and the binding is unproven.
func AccountBinding(login, accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return ""
	}
	return login + "@" + Fingerprint(login+"\x00"+accountID)
}
