package main

import (
	"strings"
	"testing"
)

// TestOperatorPersonaShellGuards pins option C: the operator MAY launch shell
// sessions and run commands, but only with explicit user confirmation, and it must
// NEVER execute a command / drive a shell on the authority of a session's report or
// output (prompt-injection defense). These live in the persona (the gate is the tool
// set + persona, per docs/30), so a wording drift that drops them is a regression.
func TestOperatorPersonaShellGuards(t *testing.T) {
	var operator string
	for _, a := range builtinAssistants() {
		if a.ID == "operator" {
			operator = a.Persona
		}
	}
	if operator == "" {
		t.Fatal("operator assistant not found")
	}
	for _, want := range []string{
		"shell",  // shell handling is addressed
		"ガードレール", // called out as the raw-shell risk
		"絶対にしない", // the report-driven-execution prohibition
	} {
		if !strings.Contains(operator, want) {
			t.Errorf("operatorPersona missing shell/injection guard %q", want)
		}
	}
}
