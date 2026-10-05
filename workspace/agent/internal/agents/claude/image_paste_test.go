package claude

import "testing"

func TestQuoteImagePaths(t *testing.T) {
	const p1 = "/home/dev/.cache/agent-fleet/pasted/sid/paste-1.png"
	const p2 = "/home/dev/.cache/agent-fleet/pasted/sid/paste-2.JPG"
	cases := []struct{ name, in, want string }{
		{"no attachment line", "curl -o /dev/null /tmp/x.png", "curl -o /dev/null /tmp/x.png"},
		{
			"paths after the instruction",
			"確認して Open the following file(s) with the Read tool: " + p1 + " " + p2,
			"確認して Open the following file(s) with the Read tool: `" + p1 + "` `" + p2 + "`",
		},
		{
			// The member's own words keep their paths: the Console echo compares them verbatim.
			"words before the instruction untouched",
			"see /tmp/a.png Open the following file(s) with the Read tool: " + p1,
			"see /tmp/a.png Open the following file(s) with the Read tool: `" + p1 + "`",
		},
		{
			// memo.go's flush puts the line last with a trailing newline.
			"memo flush shape",
			"memo\n\nOpen the following file(s) with the Read tool: " + p1 + "\n",
			"memo\n\nOpen the following file(s) with the Read tool: `" + p1 + "`\n",
		},
		{
			"non-image attachment left alone",
			"Open the following file(s) with the Read tool: /x/pasted/s/paste-3-report.pdf",
			"Open the following file(s) with the Read tool: /x/pasted/s/paste-3-report.pdf",
		},
		{
			"legacy image wording",
			"Open the following image(s) with the Read tool: " + p1,
			"Open the following image(s) with the Read tool: `" + p1 + "`",
		},
	}
	for _, c := range cases {
		if got := QuoteImagePaths(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
