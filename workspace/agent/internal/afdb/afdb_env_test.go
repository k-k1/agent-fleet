package afdb

import (
	"reflect"
	"testing"
)

func TestWithoutSessionNameKeepsEverythingElse(t *testing.T) {
	got := withoutSessionName([]string{"PATH=/bin", "AF_SESSION_NAME=s1", "AF_SESSION_NAMES=x", "HOME=/h"})
	want := []string{"PATH=/bin", "AF_SESSION_NAMES=x", "HOME=/h"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withoutSessionName = %v, want %v", got, want)
	}
}
