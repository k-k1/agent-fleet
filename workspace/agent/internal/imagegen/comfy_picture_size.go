package imagegen

import (
	"bytes"
	"encoding/binary"
	"image"

	_ "golang.org/x/image/webp" // uploads accept .webp, and without this its size reads as zero
)

// comfyPictureSize answers the width and height ComfyUI's LoadImage will see for these bytes.
//
// That is the decoded size with the EXIF orientation applied: LoadImage calls
// ImageOps.exif_transpose (nodes.py), while image.DecodeConfig ignores the tag. A phone JPEG shot
// in portrait is typically stored landscape with Orientation 6, so the raw header says 4032x3024
// for a picture ComfyUI loads as 3024x4032 — and a size computed from the header would squeeze a
// portrait picture into a landscape frame.
//
// ok is false when the format cannot be decoded at all; the caller decides whether a picture of
// unknown size is acceptable.
func comfyPictureSize(raw []byte) (int, int, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, false
	}
	// Orientations 5 to 8 are the four that rotate by a quarter turn.
	if o := exifOrientation(raw); o >= 5 && o <= 8 {
		return cfg.Height, cfg.Width, true
	}
	return cfg.Width, cfg.Height, true
}

// exifOrientation reads EXIF tag 0x0112 from a JPEG (APP1), PNG (eXIf) or WebP (EXIF chunk) — the
// three containers PIL's getexif() reads it from. 1 ("as stored") is the answer for anything that
// carries no tag or cannot be parsed, which is also what ComfyUI does with such a file.
func exifOrientation(raw []byte) int {
	var tiff []byte
	switch {
	case len(raw) > 4 && raw[0] == 0xFF && raw[1] == 0xD8:
		tiff = jpegExif(raw)
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		tiff = pngExif(raw)
	case len(raw) > 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		tiff = webpExif(raw)
	}
	return tiffOrientation(tiff)
}

func jpegExif(raw []byte) []byte {
	for i := 2; i+4 <= len(raw); {
		if raw[i] != 0xFF {
			return nil
		}
		marker := raw[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2 // standalone markers carry no length
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // image data begins: no more metadata segments
			return nil
		}
		n := int(binary.BigEndian.Uint16(raw[i+2:]))
		if n < 2 || i+2+n > len(raw) {
			return nil
		}
		seg := raw[i+4 : i+2+n]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return seg[6:]
		}
		i += 2 + n
	}
	return nil
}

func pngExif(raw []byte) []byte {
	for i := 8; i+8 <= len(raw); {
		n := int(binary.BigEndian.Uint32(raw[i:]))
		if i+8+n > len(raw) {
			return nil
		}
		if string(raw[i+4:i+8]) == "eXIf" {
			return raw[i+8 : i+8+n]
		}
		i += 12 + n // length, type, data, CRC
	}
	return nil
}

func webpExif(raw []byte) []byte {
	for i := 12; i+8 <= len(raw); {
		n := int(binary.LittleEndian.Uint32(raw[i+4:]))
		if i+8+n > len(raw) {
			return nil
		}
		if string(raw[i:i+4]) == "EXIF" {
			// Some writers keep the JPEG-style preamble inside the chunk.
			return bytes.TrimPrefix(raw[i+8:i+8+n], []byte("Exif\x00\x00"))
		}
		i += 8 + n + n%2 // chunks are padded to an even length
	}
	return nil
}

// tiffOrientation reads tag 0x0112 from IFD0 of a TIFF-structured EXIF block.
func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(tiff[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(tiff[2:]) != 42 {
		return 1
	}
	ifd := int(bo.Uint32(tiff[4:]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 1
	}
	count := int(bo.Uint16(tiff[ifd:]))
	for k := range count {
		e := ifd + 2 + 12*k
		if e+12 > len(tiff) {
			return 1
		}
		// Tag 0x0112, type SHORT (3): the value sits in the first two bytes of the value field.
		if bo.Uint16(tiff[e:]) == 0x0112 && bo.Uint16(tiff[e+2:]) == 3 {
			if o := int(bo.Uint16(tiff[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}
