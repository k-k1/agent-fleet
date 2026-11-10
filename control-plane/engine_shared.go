package main

// engine_shared.go — small helpers the engine files share with the rest of the package
// (hour bucket layout, idle-window bounds, environment lookup), kept here so no engine file
// has to reach into the TTS or usage files for them.

import (
	"os"
	"strings"
)

// usageHourFmt is the hourly bucket key (docs/log/83). UTC, like the day bucket —
// the client shifts it to local time for display, because a 24-row heatmap drawn in
// UTC tells a reader in Tokyo that they work at four in the morning.
const usageHourFmt = "2006-01-02T15"

const (
	engineIdleMinSeconds = 60
	engineIdleMaxSeconds = 24 * 60 * 60
)

// firstEnv returns the first environment variable that has a value; whitespace-only counts
// as unset.
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
