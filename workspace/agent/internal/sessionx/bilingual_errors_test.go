package sessionx

import (
	"strings"
	"testing"
	"unicode"
)

func cjk(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

// The driver-selection refusals and the two sentinels below reach the Console or an operator's
// MCP tool as free text; the Agent does not know the language, so each is a "ja / en" pair.
func TestDriverRefusalTextsAreBilingual(t *testing.T) {
	for name, s := range map[string]string{
		"msgDriverUnavailable": msgDriverUnavailable,
		"msgDriverUnsupported": msgDriverUnsupported,
		"msgNoTerminalRoute":   msgNoTerminalRoute,
		"msgBusySwitch":        msgBusySwitch,
		"msgCodexStopFirst":    msgCodexStopFirst,
		"errDriverSwitching":   errDriverSwitching.Error(),
		"errPendingApproval":   errPendingApproval.Error(),
	} {
		ja, en, ok := strings.Cut(s, " / ")
		if !ok || !cjk(ja) || en == "" || cjk(en) {
			t.Errorf("%s = %q, want a \"ja / en\" pair", name, s)
		}
	}
}
