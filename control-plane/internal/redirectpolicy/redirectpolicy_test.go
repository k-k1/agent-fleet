package redirectpolicy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func p(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestOriginNormalisesDefaultPort(t *testing.T) {
	if Origin(p("https://Example.com/a")) != Origin(p("https://example.com:443/b")) {
		t.Error("default https port should compare equal")
	}
	for _, o := range []string{"http://example.com", "https://example.com:8443", "https://sub.example.com"} {
		if Origin(p("https://example.com")) == Origin(p(o)) {
			t.Errorf("%s must differ", o)
		}
	}
}

func TestOriginKeepsIPv6ZoneCase(t *testing.T) {
	if Origin(p("http://[fe80::1%25ethA]/")) == Origin(p("http://[fe80::1%25etha]/")) {
		t.Error("zones differing in case are different interfaces")
	}
	if Origin(p("http://[FE80::AB]/")) != Origin(p("http://[fe80::ab]/")) {
		t.Error("IPv6 hex case must not matter")
	}
}

func TestOriginDoesNotFoldUnicodeHosts(t *testing.T) {
	if Origin(p("http://İ.example.test/")) == Origin(p("http://i.example.test/")) {
		t.Error("U+0130 must not fold onto ASCII i")
	}
	if Origin(p("https://EXAMPLE.test/")) != Origin(p("https://example.test/")) {
		t.Error("ASCII case must fold")
	}
}

func chain(method string, urls ...string) (*http.Request, []*http.Request) {
	var via []*http.Request
	for _, u := range urls[:len(urls)-1] {
		r, _ := http.NewRequest(method, u, nil)
		via = append(via, r)
	}
	req, _ := http.NewRequest(method, urls[len(urls)-1], nil)
	return req, via
}

func TestSameOriginReadOnlyDecisions(t *testing.T) {
	cases := []struct {
		name   string
		method string
		urls   []string
		refuse bool
	}{
		{"same origin GET", "GET", []string{"https://h/a", "https://h/b"}, false},
		{"same origin HEAD", "HEAD", []string{"https://h/a", "https://H:443/b"}, false},
		{"another port", "GET", []string{"https://h/a", "https://h:8443/b"}, true},
		{"subdomain", "GET", []string{"https://h/a", "https://s.h/b"}, true},
		{"downgrade", "GET", []string{"https://h/a", "http://h/b"}, true},
		{"upgrade is another origin", "GET", []string{"http://h/a", "https://h/b"}, true},
		{"POST same origin", "POST", []string{"https://h/a", "https://h/b"}, true},
		{"PUT same origin", "PUT", []string{"https://h/a", "https://h/b"}, true},
	}
	for _, tc := range cases {
		req, via := chain(tc.method, tc.urls...)
		err := SameOriginReadOnly(req, via)
		if (err != nil) != tc.refuse || (err != nil && !IsRefused(err)) {
			t.Errorf("%s: err = %v, refuse = %v", tc.name, err, tc.refuse)
		}
	}
	long := make([]string, 11)
	for i := range long {
		long[i] = "https://h/x"
	}
	req, via := chain("GET", long...)
	if !IsRefused(SameOriginReadOnly(req, via)) {
		t.Error("the hop limit must be refused with the sentinel")
	}
}

// Through a real client: the second server sees nothing.
func TestClientNeverReachesSecondServer(t *testing.T) {
	var hits int
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer other.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	c := &http.Client{CheckRedirect: SameOriginReadOnly}
	if _, err := c.Get(src.URL); !IsRefused(err) {
		t.Errorf("err = %v", err)
	}
	if hits != 0 {
		t.Errorf("second server saw %d requests", hits)
	}
}
