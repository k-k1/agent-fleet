package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// Several git invocations hitting an expired Bitbucket token at once make one refresh
// grant, and every one of them is answered with the renewed token.
func TestCredHelperBitbucketRefreshIsSerialized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	var grants int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&grants, 1)
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"r1","expires_in":3600}`))
	}))
	defer srv.Close()
	if err := secrets.Update(func(d *secrets.Data) error {
		d.GitOAuthBridge = &secrets.CPBridge{BaseURL: srv.URL, Token: "afo_tok"}
		d.Bitbucket = &secrets.BitbucketCreds{AccessToken: "old", RefreshToken: "r0", Expiry: 1}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	outs := make([]bytes.Buffer, 3)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			credHelperGet(strings.NewReader("protocol=https\nhost=bitbucket.org\n\n"), &outs[i])
		}(i)
	}
	wg.Wait()
	if n := atomic.LoadInt32(&grants); n != 1 {
		t.Fatalf("grant calls = %d, want 1", n)
	}
	for i := range outs {
		if want := "username=x-token-auth\npassword=fresh\n"; outs[i].String() != want {
			t.Fatalf("helper %d answered %q", i, outs[i].String())
		}
	}
}
