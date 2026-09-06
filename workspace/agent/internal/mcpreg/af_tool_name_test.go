package mcpreg

import "testing"

// The server name rotates every boot, so the transcript layer has to recognise af's tools by
// shape. The `mcp__<server>__<tool>` spelling is claude's, read out of a live session.
func TestIsAFToolName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"mcp__af_40ed9852__generate_image", true},
		{"mcp__af__generate_image", true},
		{"af_40ed9852__generate_image", true},
		{"af_40ed9852_generate_image", true},
		{"generate_image", true},
		// Another server's tool of the same name must never be mistaken for af's: the whole
		// point of the rotation is that only af wears this shape.
		{"mcp__pictures__generate_image", false},
		{"mcp__af_40ed9852__af_report", false},
		{"mcp__afterburner__generate_image", false},
		{"", false},
	} {
		if got := IsAFToolName(tc.name, "generate_image"); got != tc.want {
			t.Errorf("IsAFToolName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// The shape a boot mints has to match, or the recogniser and the minter have drifted
	// apart. Checked against afNameRE rather than AFServerName(), which caches the name it
	// reads from the real home into package state and would leak into every other test in
	// this package (it did, once).
	const minted = "af_0123abcd"
	if !afNameRE.MatchString(minted) {
		t.Fatalf("%q is not the shape the minter produces — this test is checking the wrong thing", minted)
	}
	if !IsAFToolName("mcp__"+minted+"__af_report", "af_report") {
		t.Errorf("a minted server name is not recognised: %q", minted)
	}
}
