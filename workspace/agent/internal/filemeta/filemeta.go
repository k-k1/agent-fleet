// Package filemeta answers "what kind of file is this", from the name alone.
//
// It is a leaf package because two families ask the same question and must get the same
// answer: the file API (what to serve a download as, what to offer a preview for) and the
// session list (how many images a session has generated — ADR 0080 decision 3). A second
// copy of the table drifts silently: an extension one side counts as an image while the
// other refuses to show it reads as "the gallery is broken", with nothing in either test
// suite to say so.
package filemeta

import (
	"path/filepath"
	"strings"
)

// ImageContentType maps a filename to its image MIME type (mirrors the Console's
// IMAGE_EXT in lib/filemeta.ts), or "" when it isn't a previewable image extension.
func ImageContentType(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	switch ext {
	case "png":
		return "image/png"
	case "apng":
		return "image/apng"
	case "jpg", "jpeg", "jfif":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "avif":
		return "image/avif"
	case "bmp":
		return "image/bmp"
	case "ico":
		return "image/x-icon"
	case "svg":
		return "image/svg+xml"
	}
	return ""
}
