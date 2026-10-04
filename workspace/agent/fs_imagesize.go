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
//   - a JPEG's EXIF orientation is applied: 5-8 mean the pixels are stored on their side, and
//     the browser draws them upright (`image-orientation: from-image` is the default), so the
//     stored width is the height the reader sees. Read from the bytes DecodeConfig already
//     consumed — the APP1 segment sits before the frame header — so it costs no extra read.

import (
	"bytes"
	"encoding/binary"
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
	var seen bytes.Buffer
	cfg, format, err := image.DecodeConfig(io.TeeReader(io.LimitReader(opened.file, imageSizeMaxHeader), &seen))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return fsImageSize{}, false
	}
	if format == "jpeg" && jpegOrientation(seen.Bytes()) >= 5 {
		return fsImageSize{W: cfg.Height, H: cfg.Width}, true
	}
	return fsImageSize{W: cfg.Width, H: cfg.Height}, true
}

// jpegOrientation returns the EXIF Orientation tag (1-8) of a JPEG's header bytes, or 0 when
// there is none or the bytes run out. It walks the markers up to the first frame header and
// reads only IFD0 of the first "Exif" APP1 segment; anything malformed is "no orientation"
// rather than an error, since the size it qualifies is already known.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 0
		}
		marker := b[i+1]
		if marker == 0xFF { // fill byte
			i++
			continue
		}
		if marker == 0xDA || (marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC) {
			return 0 // start of scan or a frame header: no APP1 before it
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return 0
		}
		seg := b[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 0
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	count := int(bo.Uint16(t[ifd:]))
	for k := 0; k < count; k++ {
		at := ifd + 2 + 12*k
		if at+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[at:]) != 0x0112 {
			continue
		}
		if v := int(bo.Uint16(t[at+8:])); v >= 1 && v <= 8 {
			return v
		}
		return 0
	}
	return 0
}
