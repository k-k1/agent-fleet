package main

import (
	"strings"
	"testing"
)

func TestWeakKeyChecksSkipsDev(t *testing.T) {
	if m, c := weakKeyChecks("dev", "short", "short"); m || c {
		t.Errorf("AUTH=dev flagged: %v %v", m, c)
	}
	if m, c := weakKeyChecks("oauth", "  ", "  "); !m || c {
		t.Errorf("whitespace master / cookie = %v %v, want true false (cookie follows decodeKey: unset)", m, c)
	}
	if m, c := weakKeyChecks("oauth", "short", ""); !m || c {
		t.Errorf("AUTH=oauth short master, unset cookie = %v %v, want true false", m, c)
	}
}

func TestWeakKeyWarningsOnWire(t *testing.T) {
	m := &manager{authMode: "dev", nativeRuntime: true, weakMasterKey: true, weakCookieSecret: true}
	got := strings.Join(m.deploymentWarnings(), ",")
	if !strings.Contains(got, deploymentWarnWeakMasterKey) || !strings.Contains(got, deploymentWarnWeakCookieSecret) {
		t.Errorf("deploymentWarnings = %q, want both weak-key codes", got)
	}
	if w := (&manager{authMode: "dev", nativeRuntime: true}).deploymentWarnings(); len(w) != 0 {
		t.Errorf("a clean manager warns: %v", w)
	}
}

// The log lines must never carry the value.
func TestWeakKeyLogsCarryNoValue(t *testing.T) {
	for _, l := range []string{weakMasterKeyLog, weakCookieSecretLog} {
		if strings.Count(l, "%") != 1 || !strings.Contains(l, "%s") {
			t.Errorf("log format takes more than the auth mode: %q", l)
		}
	}
}
