package main

import (
	"context"
	"testing"
)

// The seam reports the build version (not a fixed string), the live brand label and the
// public origin.
func TestCPDepsServerIdentity(t *testing.T) {
	old := buildVersion
	buildVersion = "9.9.9"
	defer func() { buildVersion = old }()
	b, _ := buildBrand("", "thyme")
	m := &manager{publicBaseURL: "https://af.example.test", brand: newBrandResolver(b, nil)}
	got := cpDeps{m}.ServerIdentity(context.Background())
	if got.Version != "9.9.9" || got.BrandLabel != "thyme" || got.PublicBaseURL != "https://af.example.test" {
		t.Fatalf("ServerIdentity = %+v", got)
	}
	if got := (cpDeps{&manager{}}).ServerIdentity(context.Background()); got.BrandLabel != "" {
		t.Fatalf("no resolver must mean no label, got %q", got.BrandLabel)
	}
}
