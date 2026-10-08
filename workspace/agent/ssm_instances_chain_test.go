package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Discovery through a role chain resolves from the isolated config alone: no shared
// credentials file, no credentials or endpoint override from the environment.
func TestSSMInstancesChainRunsIsolated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	seen := filepath.Join(bin, "seen")
	script := "#!/bin/sh\nenv | grep -E '^AWS_' | sort > " + seen + "\necho '{\"InstanceInformationList\":[]}'\n"
	if err := os.WriteFile(filepath.Join(bin, "aws"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIALEAK")
	t.Setenv("AWS_ENDPOINT_URL", "http://evil")
	body := `{"Profile":"deploy","Region":"eu-west-1","StartURL":"https://example.awsapps.com/start","SSORegion":"us-east-1","AccountID":"123456789012","RoleName":"Dev",` +
		`"SourceProfile":"src","RoleARN":"arn:aws:iam::210987654321:role/deploy"}`
	rec := httptest.NewRecorder()
	handleSSMInstances(rec, httptest.NewRequest(http.MethodPost, "/ssm/instances", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	b, _ := os.ReadFile(seen)
	got := string(b)
	if strings.Contains(got, "AKIALEAK") || strings.Contains(got, "AWS_ENDPOINT_URL=") || !strings.Contains(got, "AWS_SHARED_CREDENTIALS_FILE=/dev/null") ||
		!strings.Contains(got, "AWS_PROFILE=deploy") {
		t.Fatalf("aws saw:\n%s", got)
	}
}
