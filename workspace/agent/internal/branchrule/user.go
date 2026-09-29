package branchrule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// UserRules is the user layer's own store (decision 2). It is not ui-prefs: the Console
// writes ui-prefs whole with only the keys it knows, so an older Console saving any
// setting would drop a key it does not know.
type UserRules struct {
	Rules []Rule `json:"rules"`
}

var userMu sync.Mutex

// ReadUser loads the store at path; a missing or unreadable file is no rules.
func ReadUser(path string) UserRules {
	userMu.Lock()
	defer userMu.Unlock()
	var u UserRules
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &u) != nil {
		return UserRules{Rules: []Rule{}}
	}
	if u.Rules == nil {
		u.Rules = []Rule{}
	}
	return u
}

// WriteUser validates u and replaces the store atomically.
func WriteUser(path string, u UserRules) error {
	if err := ValidateRules(u.Rules, true); err != nil {
		return err
	}
	if u.Rules == nil {
		u.Rules = []Rule{}
	}
	b, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return err
	}
	userMu.Lock()
	defer userMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ErrBareStarName refuses a bare-`*` rule that sets `name`: the user's default template
// has one home, workItemBranchTemplate, and nothing may shadow it.
var ErrBareStarName = errors.New("a rule matching every repository (*) cannot set name; the default template is workItemBranchTemplate")

// ValidateRules runs the key and ref-name checks a user or tenant rule goes through.
func ValidateRules(rules []Rule, user bool) error {
	for i, r := range rules {
		m := strings.TrimSpace(r.Match)
		if !ValidMatch(m) {
			return fmt.Errorf("rule %d: match %s is not a host/owner/repo pattern", i+1, quote(r.Match))
		}
		if user && m == "*" && r.Name != nil {
			return fmt.Errorf("rule %d: %w", i+1, ErrBareStarName)
		}
		if r.Base != nil && !ValidBase(*r.Base) {
			return fmt.Errorf("rule %d: base %s is not a branch name", i+1, quote(*r.Base))
		}
		for k, kr := range r.Types {
			if !ValidKind(k) {
				return fmt.Errorf("rule %d: kind %s is not in the vocabulary", i+1, quote(k))
			}
			if kr.Prefix != nil && !ValidPrefix(*kr.Prefix) {
				return fmt.Errorf("rule %d: %s prefix %s is not a branch prefix", i+1, k, quote(*kr.Prefix))
			}
			if kr.Base != nil && !ValidBase(*kr.Base) {
				return fmt.Errorf("rule %d: %s base %s is not a branch name", i+1, k, quote(*kr.Base))
			}
		}
	}
	return nil
}

// UserLayer is the stored rules plus the default template read as the user's `*` rule.
func UserLayer(rules []Rule, template string) Layer {
	l := Layer{Name: "user"}
	for _, r := range rules {
		r.Source = r.Match
		l.Rules = append(l.Rules, r)
	}
	if t := strings.TrimSpace(template); t != "" {
		l.Rules = append(l.Rules, Rule{Match: "*", Name: str(t), Source: "workItemBranchTemplate"})
	}
	return l
}
