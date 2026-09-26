package main

import (
	"os"
	"strings"
	"testing"
)

// /dev/null is a character device; a redirected run must not count as interactive.
func TestIsTerminalRejectsDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("/dev/null was treated as a terminal")
	}
}

func TestParseAWSExecArgs(t *testing.T) {
	o, list := parseAWSExecArgs([]string{"--profile=prod", "--account", "123456789012", "--keep-aws-config",
		"--region", "eu-west-1", "-q", "--no-login", "--", "cdk", "deploy", "--profile", "other"})
	if list || o.Profile != "prod" || o.Account != "123456789012" || !o.KeepConfig || o.Region != "eu-west-1" ||
		!o.Quiet || o.Login != "never" || strings.Join(o.Argv, " ") != "cdk deploy --profile other" {
		t.Fatalf("parsed %+v list=%v", o, list)
	}
	if o, list := parseAWSExecArgs([]string{"--list"}); !list || o.Login != "auto" {
		t.Fatalf("--list: %+v %v", o, list)
	}
}
