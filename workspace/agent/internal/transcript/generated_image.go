package transcript

// Recognising af's own generate_image tool in a session transcript (ADR 0069).
//
// The image the tool produced is on a disk the Console can already serve, but nothing in the
// conversation says so: what the model sees is a JSON result, and what the user sees is a
// faint tool trace. Turning that into a `kind:"userfile"` part is the whole integration —
// the mirror's existing FileCard then renders a thumbnail with no frontend change, the same
// way codex's own image_gen and view_image results are surfaced.
//
// This lives here, in the vocabulary every kind's parser shares, because the tool result is
// the same JSON whichever CLI called it. What differs per kind is only where the parser can
// GET that result, which is why each parser calls in rather than this file walking anything.

import (
	"encoding/json"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

// GenerateImageTool is the tool name, unnamespaced. A client prefixes it with the af server's
// per-boot name (mcpreg.IsAFToolName).
const GenerateImageTool = "generate_image"

// GeneratedImageFiles pulls the produced file paths out of one generate_image tool RESULT.
// nil means "not that tool, or nothing to show" — every caller treats it as "leave the trace
// as it was".
//
// The paths come from the result rather than from a directory listing on purpose: the result
// is what the tool actually produced for THIS call, so a session that generated three images
// over a conversation gets three cards in the right places, and a failed call gets none.
func GeneratedImageFiles(toolName, result string) []string {
	files, _ := generatedImageResult(toolName, result)
	return files
}

func generatedImageResult(toolName, result string) (files, warnings []string) {
	if !mcpreg.IsAFToolName(toolName, GenerateImageTool) {
		return nil, nil
	}
	result = strings.TrimSpace(result)
	if result == "" || result[0] != '{' {
		// An error result is prose, not JSON. There is no file, and inventing a card for one
		// would be the exact failure ADR 0069 designed the collector to avoid.
		return nil, nil
	}
	var out struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
		Warnings []string `json:"warnings"`
	}
	if json.Unmarshal([]byte(result), &out) != nil {
		return nil, nil
	}
	for _, f := range out.Files {
		if p := strings.TrimSpace(f.Path); p != "" {
			files = append(files, p)
		}
	}
	for _, w := range out.Warnings {
		if w = strings.TrimSpace(w); w != "" {
			warnings = append(warnings, w)
		}
	}
	return files, warnings
}

// GeneratedImagePart builds the sibling part for a generate_image call, or ok=false when the
// result carries no file.
//
// The caption carries the WARNINGS and nothing else. What a provider could not honour is
// reported, not hidden (ADR 0069 decision 7), and until now that report reached only the
// model: the user saw a picture with no hint that they had asked for 1024x1024 and been given
// 1536x1024. Repeating the prompt here would be noise — the tool trace directly above already
// shows it.
func GeneratedImagePart(toolName, result string) (Part, bool) {
	files, warnings := generatedImageResult(toolName, result)
	if len(files) == 0 {
		return Part{}, false
	}
	return Part{Kind: "userfile", Tool: toolName, Files: files, Caption: strings.Join(warnings, " / ")}, true
}

// PendingImage is a picture card waiting to be put after the tool trace at index At.
type PendingImage struct {
	At   int
	Part Part
}

// SplicePendingImages inserts each card immediately after its own tool trace, walking from
// the end so the earlier insertion points stay valid.
//
// Collect-then-splice, never insert-as-you-go: every parser that pairs a result back to its
// call keeps an index into this same slice (toolIdx / toolUseId → position), and inserting
// mid-walk would silently move the rows those indices point at — the next tool's output would
// land on the previous tool's picture.
func SplicePendingImages(parts []Part, pending []PendingImage) []Part {
	for i := len(pending) - 1; i >= 0; i-- {
		p := pending[i]
		if p.At < 0 || p.At >= len(parts) {
			continue
		}
		parts = append(parts[:p.At+1], append([]Part{p.Part}, parts[p.At+1:]...)...)
	}
	return parts
}
