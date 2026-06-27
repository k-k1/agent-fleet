package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

func TestLocalCustodian(t *testing.T) {
	ctx := context.Background()
	m := sha256.Sum256([]byte("test-master"))
	c := newLocalCustodian(m[:])

	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	ct, err := c.Wrap(ctx, "tenant-a", dek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	got, err := c.Unwrap(ctx, "tenant-a", ct)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatalf("roundtrip mismatch")
	}
	// A blob wrapped for tenant-a must not unwrap as tenant-b.
	if _, err := c.Unwrap(ctx, "tenant-b", ct); err == nil {
		t.Fatalf("expected failure unwrapping with wrong keyRef")
	}
}
