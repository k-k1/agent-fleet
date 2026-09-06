package transcript

import (
	"reflect"
	"testing"
)

func TestGeneratedImageFiles(t *testing.T) {
	const result = `{"files":[{"path":"/home/u/.cache/agent-fleet/generated/sid/image-1.png","name":"image-1.png","width":1536,"height":1024}],"provider":"codex","warnings":["size=1024x1024 requested, 1536x1024 produced"]}`
	for _, tc := range []struct {
		name, tool, result string
		want               []string
	}{
		{name: "af's tool, one file", tool: "mcp__af_40ed9852__generate_image", result: result,
			want: []string{"/home/u/.cache/agent-fleet/generated/sid/image-1.png"}},
		// Another server's tool with the same shape of result is not af's picture, and a card
		// under it would attribute someone else's file to this feature.
		{name: "another server's tool", tool: "mcp__pictures__generate_image", result: result},
		{name: "a different af tool", tool: "mcp__af_40ed9852__af_report", result: result},
		// A refusal comes back as prose. There is no file, and inventing a card for one is
		// exactly the failure the collector was designed to avoid (ADR 0069 decision 4).
		{name: "an error result", tool: "mcp__af_40ed9852__generate_image",
			result: "画像を生成できませんでした: codex is not logged in"},
		{name: "empty result", tool: "mcp__af_40ed9852__generate_image", result: ""},
		{name: "no files in the result", tool: "mcp__af_40ed9852__generate_image", result: `{"provider":"codex","files":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := GeneratedImageFiles(tc.tool, tc.result)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("files = %v, want %v", got, tc.want)
			}
			part, ok := GeneratedImagePart(tc.tool, tc.result)
			if ok != (len(tc.want) > 0) {
				t.Fatalf("part ok = %v, want %v", ok, len(tc.want) > 0)
			}
			if ok && (part.Kind != "userfile" || !reflect.DeepEqual(part.Files, tc.want)) {
				t.Fatalf("part = %+v", part)
			}
			// The warnings are what tell the user the size they asked for is not the size they
			// got. Reaching only the model — which is where they stopped before — is how a
			// caption-less card quietly implies the request was honoured.
			if ok && part.Caption != "size=1024x1024 requested, 1536x1024 produced" {
				t.Fatalf("caption = %q, want the warning", part.Caption)
			}
		})
	}
}

// Several images from one call become one card holding all of them, the way SendUserFile's
// multi-file panel already does.
func TestGeneratedImageFilesKeepsOrder(t *testing.T) {
	got := GeneratedImageFiles("mcp__af_40ed9852__generate_image",
		`{"files":[{"path":"/a/1.png"},{"path":""},{"path":"/a/2.png"}]}`)
	if want := []string{"/a/1.png", "/a/2.png"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}
