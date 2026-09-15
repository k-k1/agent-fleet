package main

// contract_engine_keys_test.go — the bucket layout is written down twice, and both copies
// decide whether a file can be loaded at all.
//
// The Console composes the destination key when a file is taken in (`engineIngestPrefix`,
// console/src/features/settings/admin/adminEngineModels.tsx) and the CP refuses any other
// spelling and computes the repair for rows already in the wrong place (engineComfyRoleDir). If
// the two ever disagree, every ingest through the form is refused — or worse, accepted into a
// directory its ComfyUI loader does not enumerate, which nothing reports until an operator waits
// out a cold start and reads "the model does not work".
//
// Pass `go test -count=1` when editing only the TS: this reads OUTSIDE the module, so the test
// binary is unchanged and `go test` answers from its cache (`ok (cached)`) for a change it never
// looked at.

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const consoleEnginePrefixTSX = "../console/src/features/settings/admin/adminEngineModels.tsx"

func TestEngineRoleDirectoriesMatchTheConsole(t *testing.T) {
	src, err := os.ReadFile(consoleEnginePrefixTSX)
	if err != nil {
		t.Fatalf("the Console's key composer is not at %s (moved? then move this check with it): %v",
			consoleEnginePrefixTSX, err)
	}
	body := string(src)
	at := strings.Index(body, "export function engineIngestPrefix")
	if at < 0 {
		t.Fatal("engineIngestPrefix is gone from the Console — the CP is now the only copy of the layout")
	}
	end := strings.Index(body[at:], "\n}")
	if end < 0 {
		t.Fatal("could not read to the end of engineIngestPrefix")
	}
	fn := body[at : at+end]

	// `case "--flag":` … `return "image/<dir>/";`, with the encoders sharing one return.
	cases := regexp.MustCompile(`case "(--[a-z_-]+)":`).FindAllStringSubmatch(fn, -1)
	if len(cases) < 4 {
		t.Fatalf("only %d flags were read out of engineIngestPrefix — the scan is broken before the check is", len(cases))
	}
	returns := regexp.MustCompile(`return "image/([a-z_]+)/";`).FindAllStringSubmatch(fn, -1)
	if len(returns) < 4 {
		t.Fatalf("only %d directories were read out of engineIngestPrefix", len(returns))
	}
	// Walk the function in source order: every flag takes the next `return` below it.
	type at2 struct {
		flag string
		pos  int
	}
	var flags []at2
	for _, m := range regexp.MustCompile(`case "(--[a-z_-]+)":`).FindAllStringSubmatchIndex(fn, -1) {
		flags = append(flags, at2{fn[m[2]:m[3]], m[0]})
	}
	dirAfter := func(pos int) string {
		m := regexp.MustCompile(`return "image/([a-z_]+)/";`).FindStringSubmatchIndex(fn[pos:])
		if m == nil {
			return ""
		}
		return fn[pos+m[2] : pos+m[3]]
	}
	for _, f := range flags {
		want := dirAfter(f.pos)
		if want == "" {
			t.Fatalf("no directory follows the %s case in the Console", f.flag)
		}
		if got := engineComfyRoleDir(f.flag, false); got != want+"/" {
			t.Errorf("%s: the CP stages it in %q and the Console in %q/ — one of them is a file no loader lists",
				f.flag, got, want)
		}
	}
	// The two answers the switch does not spell as a case: the whole checkpoint (`default`) and
	// an adapter (the early return above the switch).
	if !strings.Contains(fn, `return "image/checkpoints/";`) || engineComfyRoleDir("", false) != "checkpoints/" {
		t.Errorf("the unflagged checkpoint: CP says %q, Console's default is not image/checkpoints/",
			engineComfyRoleDir("", false))
	}
	if !strings.Contains(fn, `isImage ? "image/loras/"`) || engineComfyRoleDir("", true) != "loras/" {
		t.Errorf("adapters: CP says %q, the Console's lora branch does not agree", engineComfyRoleDir("", true))
	}
	// And every role the vocabulary has must be one the Console can actually compose a key for:
	// a flag with no case falls into `default` and the file lands in `checkpoints/`.
	for _, flag := range engineComfyFileFlags {
		if flag == "" {
			continue
		}
		if !strings.Contains(fn, `case "`+flag+`":`) {
			t.Errorf("%s has no case in the Console, so a file taken in under it is staged in checkpoints/", flag)
		}
	}
}
