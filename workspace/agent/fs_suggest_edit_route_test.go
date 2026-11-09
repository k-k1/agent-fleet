package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFSSuggestEditRouteRegistered(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/fs/suggest-edit", nil)
	_, pattern := buildMux().Handler(req)
	if pattern != "POST /fs/suggest-edit" {
		t.Fatalf("route pattern=%q", pattern)
	}
}
