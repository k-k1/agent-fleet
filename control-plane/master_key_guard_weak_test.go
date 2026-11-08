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

func TestWeakKeyRefusal(t *testing.T) {
	strong := "Zm9vYmFyYmF6cXV4MDEyMzQ1Njc4OWFiY2RlZmdoaWprbG1ub3A"
	for _, tc := range []struct {
		name, auth, master, cookie string
		refuse                     bool
		mentions                   []string
	}{
		{"oauth weak master", "oauth", "wk-master-1", strong, true, []string{"AF_MASTER_KEY"}},
		{"proxy weak cookie", "proxy", strong, "wk-cookie-1", true, []string{"AF_COOKIE_SECRET"}},
		{"oauth both weak", "oauth", "aaaa-m", "bbbb-c", true, []string{"AF_MASTER_KEY", "AF_COOKIE_SECRET"}},
		{"oauth strong", "oauth", strong, strong, false, nil},
		{"oauth unset master", "oauth", "", strong, false, nil},
		{"dev weak", "dev", "short", "short", false, nil},
	} {
		m, c := weakKeyChecks(tc.auth, tc.master, tc.cookie)
		err := weakKeyRefusal(tc.auth, m, c)
		if (err != nil) != tc.refuse {
			t.Errorf("%s: err = %v, want refuse=%v", tc.name, err, tc.refuse)
			continue
		}
		for _, n := range tc.mentions {
			if !strings.Contains(err.Error(), n) {
				t.Errorf("%s: %q does not name %s", tc.name, err, n)
			}
		}
		if err != nil {
			for _, v := range []string{tc.master, tc.cookie} {
				if strings.Contains(err.Error(), v) {
					t.Errorf("%s: the message carries a key value", tc.name)
				}
			}
			if !strings.Contains(err.Error(), "guide/operate/04-secure.md") {
				t.Errorf("%s: no pointer to the guide: %q", tc.name, err)
			}
		}
	}
}
