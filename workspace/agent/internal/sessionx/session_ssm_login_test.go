package sessionx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/tmuxx"
)

// TestSSMLoginStatusJoinsWrappedURL pins #1025: the login modal reads the device URL off the
// session's tmux pane, and a narrow client (a phone, a split pane, or one attaching mid-login
// and resizing the window) wraps it. Without `capture-pane -J` the regex took the first
// wrapped row as the whole URL, so the modal's sign-in button opened a broken link.
func TestSSMLoginStatusJoinsWrappedURL(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	isolateAgentState(t)
	const name = "ssmwrap"
	const url = "https://device.sso.ap-northeast-1.amazonaws.com/?user_code=ABCD-EFGH"
	tn := session.TmuxName(name)
	// 30 columns: the URL (68 chars) wraps over three rows.
	out, err := tmuxx.Cmd("new-session", "-d", "-s", tn, "-x", "30", "-y", "24",
		"sh", "-c", fmt.Sprintf("printf '%%s\\n' 'Then enter the code:' 'ABCD-EFGH' %q; sleep 60", url)).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session: %v\n%s", err, out)
	}
	session.WriteMeta(session.Meta{Name: name, Dir: t.TempDir(), Kind: session.KindSSM})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /sessions/{name}/ssm-login", HandleSSMLoginStatus)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var got ssmLoginStatus
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := http.Get(srv.URL + "/sessions/" + name + "/ssm-login")
		if err != nil {
			t.Fatal(err)
		}
		got = ssmLoginStatus{}
		err = json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		// Wait for printf to reach the pane: the phase stays "pending" until the URL shows.
		if got.Phase != "pending" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.Phase != "authorize" || got.URL != url || got.Code != "ABCD-EFGH" {
		t.Fatalf("ssm-login = %+v, want phase authorize, url %q, code ABCD-EFGH", got, url)
	}
	if strings.Contains(got.URL, "\n") {
		t.Fatalf("url carries a newline: %q", got.URL)
	}
}

// TestDeviceAuthorizationFindsThePortalURL: an issuer-form start URL gets the access
// portal back, not device.sso, and the code-carrying URL is the one to pick.
func TestDeviceAuthorizationFindsThePortalURL(t *testing.T) {
	out := "Browser will not be automatically opened.\nPlease visit the following URL:\n\n" +
		"https://d-0123456789.awsapps.com/start/#/device\n\nThen enter the code:\n\nABCD-EFGH\n\n" +
		"Alternatively, you may visit the following URL which will autofill the code upon loading:\n" +
		"https://d-0123456789.awsapps.com/start/#/device?user_code=ABCD-EFGH\n"
	url, code := DeviceAuthorization(out)
	if url != "https://d-0123456789.awsapps.com/start/#/device?user_code=ABCD-EFGH" || code != "ABCD-EFGH" {
		t.Fatalf("url, code = %q, %q", url, code)
	}
}

// TestDeviceAuthorizationFindsTheSigninURL: aws-cli 2.36 prints its URLs on
// <region>.signin.aws, which neither the user_code nor the device.sso pattern matches.
func TestDeviceAuthorizationFindsTheSigninURL(t *testing.T) {
	head := "Browser will not be automatically opened.\nPlease visit the following URL:\n\n" +
		"https://us-east-1.signin.aws/device\n\nThen enter the code:\n\nABCD-EFGH\n\n" +
		"Alternatively, you may visit the following URL which will autofill the code upon loading:\n"
	full := head + "https://us-east-1.signin.aws/device?code=ABCD-EFGH\n"
	if url, code := DeviceAuthorization(full); url != "https://us-east-1.signin.aws/device?code=ABCD-EFGH" || code != "ABCD-EFGH" {
		t.Fatalf("url, code = %q, %q", url, code)
	}
	// The autofill URL is still arriving: show the complete plain one, never a cut one.
	if url, _ := DeviceAuthorization(head + "https://us-east-1.signin.aws/dev"); url != "https://us-east-1.signin.aws/device" {
		t.Fatalf("partial autofill URL: got %q", url)
	}
}

// TestDeviceAuthorizationSigninURLEdges pins what the signin.aws pattern takes and refuses.
// The fixture URLs are stand-ins for the observed aws-cli 2.36 output, whose query part the
// log redacted; only the host and the line layout are observed.
func TestDeviceAuthorizationSigninURLEdges(t *testing.T) {
	cases := []struct{ name, out, want string }{
		{"crlf", "https://us-east-1.signin.aws/device\r\nThen enter the code:\r\nABCD-EFGH\r\n", "https://us-east-1.signin.aws/device"},
		{"no trailing newline", "https://us-east-1.signin.aws/device", ""},
		{"quoted", `{"url": "https://us-east-1.signin.aws/device"}`, "https://us-east-1.signin.aws/device"},
		{"signin.aws inside another host's path", "https://evil.example/x.signin.aws\n", ""},
		{"host that only starts with signin.aws", "https://x.signin.awsevil.example/\n", ""},
		{"user_code URL wins", "https://us-east-1.signin.aws/device\nhttps://d-1.awsapps.com/start/#/device?user_code=ABCD-EFGH\n", "https://d-1.awsapps.com/start/#/device?user_code=ABCD-EFGH"},
		{"device.sso wins over signin.aws", "https://us-east-1.signin.aws/device\nhttps://device.sso.us-east-1.amazonaws.com/\n", "https://device.sso.us-east-1.amazonaws.com/"},
	}
	for _, c := range cases {
		if got, _ := DeviceAuthorization(c.out); got != c.want {
			t.Errorf("%s: url = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDeviceAuthorizationRefusesACutURL: the user_code and device.sso patterns hold back a
// URL that ends exactly at the end of the output, like the signin.aws one, and show it once
// a delimiter follows.
func TestDeviceAuthorizationRefusesACutURL(t *testing.T) {
	const withCode = "https://d-1.awsapps.com/start/#/device?user_code=ABCD-EFGH"
	const device = "https://device.sso.us-east-1.amazonaws.com/"
	cases := []struct{ name, out, want string }{
		{"user_code cut", "ABCD-EFGH\n" + withCode, ""},
		{"user_code complete", "ABCD-EFGH\n" + withCode + "\n", withCode},
		{"user_code quoted", `{"u": "` + withCode + `"}`, withCode},
		{"device.sso cut", device, ""},
		{"device.sso complete", device + "\n", device},
		{"cut user_code leaves the complete device.sso one", device + "\n" + withCode, device},
	}
	for _, c := range cases {
		if got, _ := DeviceAuthorization(c.out); got != c.want {
			t.Errorf("%s: url = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestParseSSMLoginURLAtPaneEnd: a pane snapshot is re-read unchanged, so a URL that ends
// it without a newline is a finished one and must be detected.
func TestParseSSMLoginURLAtPaneEnd(t *testing.T) {
	const url = "https://d-1.awsapps.com/start/#/device?user_code=ABCD-EFGH"
	got := parseSSMLogin("Then enter the code:\nABCD-EFGH\n"+url, true)
	if got.Phase != "authorize" || got.URL != url || got.Code != "ABCD-EFGH" {
		t.Fatalf("parseSSMLogin = %+v, want authorize with %q", got, url)
	}
}
