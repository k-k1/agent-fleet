package main

// ADR 0083 decision 5: a row naming an images provider this Agent build does not implement must
// say so out loud rather than leaving generate_image to vanish from tools/list with nothing in
// the log.

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func captureEngineLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestLogUnservableImageRowsNamesAnUnknownProvider(t *testing.T) {
	buf := captureEngineLog(t)
	logUnservableImageRows([]engineCatalogRow{
		{Key: "image", API: "images", Provider: "sdcpp"},
	})
	if !strings.Contains(buf.String(), "image") || !strings.Contains(buf.String(), `"sdcpp"`) {
		t.Errorf("log = %q, want the row's key and provider named", buf.String())
	}
}

func TestLogUnservableImageRowsStaysQuietForKnownProviders(t *testing.T) {
	buf := captureEngineLog(t)
	logUnservableImageRows([]engineCatalogRow{
		{Key: "image", API: "images", Provider: "comfy"},
		{Key: "oai-image", API: "images", Provider: "openai-compat"},
	})
	if buf.Len() != 0 {
		t.Errorf("log = %q, want silence for providers this build implements", buf.String())
	}
}

func TestLogUnservableImageRowsIgnoresChatRows(t *testing.T) {
	buf := captureEngineLog(t)
	logUnservableImageRows([]engineCatalogRow{
		{Key: "llm", API: "chat", Provider: "llamacpp"},
	})
	if buf.Len() != 0 {
		t.Errorf("log = %q, want the chat role left alone", buf.String())
	}
}
