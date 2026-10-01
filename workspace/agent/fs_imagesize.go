package main

// A picture's width and height, read from its header (POST /fs/imagesize, ADR 0080 decision 16).
//
// The gallery's cards show a downscaled copy, so a thumbnail's naturalWidth is the size AFTER
// an integer downscale — and because a small image is served as the original, it is sometimes
// right, which is worse than always wrong. The real size is in the first bytes of the file:
// image.DecodeConfig reads the header and stops, so a 3 MB PNG costs a few dozen bytes.
//
// Rules that keep it cheap and no wider than a download:
//
//   - every path goes through the SAME gate as /fs/download (resolveFDReadPath + openFDFile:
//     canonical spelling, browse root and denylist, the read-only roots, openat2 without
//     symlinks). A path that fails any of it is simply absent from the answer, as is a file
//     whose header cannot be read — the caller draws nothing rather than an error.
//   - the read is bounded (imageSizeMaxHeader). A JPEG whose frame header sits behind more
//     metadata than that answers "unknown" instead of reading on into the file.
//   - one request answers many paths (imageSizeMaxPaths), so a folder's cards cost one round
//     trip per batch rather than one per card.

import (
	"image"
	_ "image/gif"  // DecodeConfig dispatches on the registered formats
	_ "image/jpeg" // (fs_thumb.go registers these too; listed so this file stands alone)
	_ "image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

const (
	imageSizeMaxBody = 128 * 1024
	// Paths answered per request; the rest are dropped and simply come back unknown. The
	// gallery batches what its cards ask for into chunks of this size (console imageSize.ts).
	imageSizeMaxPaths = 100
	// Bytes read per file at most. PNG, GIF and WebP put the size in the first 30 bytes; a JPEG
	// puts it in the frame header after the APPn segments (EXIF, ICC, XMP), each at most 64 KiB.
	imageSizeMaxHeader = 256 << 10
)

type fsImageSizeRequest struct {
	Paths []string `json:"paths"`
}

type fsImageSize struct {
	W int `json:"w"`
	H int `json:"h"`
}

// fsImageSizeResponse is keyed by the path exactly as the request spelled it.
type fsImageSizeResponse struct {
	Sizes map[string]fsImageSize `json:"sizes"`
}

func handleFSImageSize(w http.ResponseWriter, r *http.Request) {
	var req fsImageSizeRequest
	if serr := httpx.DecodeStrictJSON(r, &req, imageSizeMaxBody); serr != nil {
		httpx.WriteErr(w, serr.Status, serr.Code, serr.Message)
		return
	}
	paths := req.Paths
	if len(paths) > imageSizeMaxPaths {
		paths = paths[:imageSizeMaxPaths]
	}
	out := fsImageSizeResponse{Sizes: map[string]fsImageSize{}}
	for _, p := range paths {
		if _, done := out.Sizes[p]; done {
			continue
		}
		if sz, ok := readImageSize(p); ok {
			out.Sizes[p] = sz
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// imageSizeExt is the set of extensions whose header is worth opening the file for. Anything
// else (svg is text, avif/bmp have no decoder here) is never opened.
func imageSizeExt(name string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "png", "apng", "jpg", "jpeg", "jfif", "gif", "webp":
		return true
	}
	return false
}

func readImageSize(input string) (fsImageSize, bool) {
	if !imageSizeExt(input) {
		return fsImageSize{}, false
	}
	path, aerr := resolveFDReadPath(input)
	if aerr != nil {
		return fsImageSize{}, false
	}
	opened, aerr := openFDFile(path)
	if aerr != nil {
		return fsImageSize{}, false
	}
	defer opened.close()
	cfg, _, err := image.DecodeConfig(io.LimitReader(opened.file, imageSizeMaxHeader))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return fsImageSize{}, false
	}
	return fsImageSize{W: cfg.Width, H: cfg.Height}, true
}
