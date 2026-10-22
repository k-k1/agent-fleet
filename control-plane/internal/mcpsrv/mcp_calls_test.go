package mcpsrv

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unsafe"
)

func TestCallLogIsBoundedAndConcurrencySafe(t *testing.T) {
	var l CallLog
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				l.add("pat:p", CallRecord{Status: i})
				_ = l.Recent("pat:p")
			}
		}()
	}
	wg.Wait()
	if n := len(l.Recent("pat:p")); n != callRingSize {
		t.Errorf("ring holds %d, want %d", n, callRingSize)
	}
	for i := 0; i < callMaxKeys+100; i++ {
		l.add("grant:"+strconv.Itoa(i), CallRecord{})
	}
	if len(l.rings) > callMaxKeys {
		t.Errorf("%d rings, want <= %d", len(l.rings), callMaxKeys)
	}
}

func TestCallLogNilAndNewestFirst(t *testing.T) {
	var nl *CallLog
	nl.add("k", CallRecord{})
	if got := nl.Recent("k"); got == nil || len(got) != 0 {
		t.Errorf("nil log: %v", got)
	}
	var l CallLog
	for i := 1; i <= 3; i++ {
		l.add("k", CallRecord{Status: i})
	}
	if got := l.Recent("k"); got[0].Status != 3 || got[2].Status != 1 {
		t.Errorf("order: %+v", got)
	}
}

func TestClipKeepsRunesWhole(t *testing.T) {
	if got := clip("あいう", 4); got != "あ" {
		t.Errorf("clip: %q", got)
	}
}

func TestClipOwnsItsBytes(t *testing.T) {
	big := strings.Repeat("a", 1<<20)
	got := clip(big, 10)
	if len(got) != 10 {
		t.Fatalf("len %d", len(got))
	}
	// A substring would share big's backing array; a clone does not.
	if unsafe.StringData(got) == unsafe.StringData(big) {
		t.Error("clip returned a view into the original string")
	}
}

func TestItemForBoundsAndFlags(t *testing.T) {
	huge := strings.Repeat("m", 1<<20)
	req := rpcRequest{ID: json.RawMessage("1"), Method: huge}
	it := itemFor(req, paramsMeta{}, mcpEra{}, rpcErr(req.ID, rpcMethodNotFound, "method not found: "+huge), 200)
	if len(it.Method) > 64 || len(it.ErrorMessage) > 120 {
		t.Errorf("not clipped: %d %d", len(it.Method), len(it.ErrorMessage))
	}
	call := rpcRequest{ID: json.RawMessage("1"), Method: "tools/call"}
	if it := itemFor(call, paramsMeta{Name: "x"}, mcpEra{}, rpcOK(call.ID, toolError("boom")), 200); !it.ToolError || it.Name != "x" || it.ErrorCode != 0 {
		t.Errorf("tool error: %+v", it)
	}
	read := rpcRequest{ID: json.RawMessage("1"), Method: "resources/read"}
	if it := itemFor(read, paramsMeta{URI: "file:///secret/medical.txt"}, mcpEra{Stateless: true, Version: mcpVersionStateless},
		rpcErr(read.ID, rpcHeaderMismatch, `header mismatch: Mcp-Name (absent) does not match body value "file:///secret/medical.txt"`), 400); it.ErrorMessage != "" || it.ErrorCode != rpcHeaderMismatch {
		t.Errorf("resource URI leaked through the error message: %+v", it)
	}
	if it := itemFor(read, paramsMeta{URI: "file:///secret"}, mcpEra{}, nil, 200); it.Name != "" {
		t.Errorf("a resource URI must not be recorded: %q", it.Name)
	}
}

func TestCallLogForgetAndParseErrorEra(t *testing.T) {
	var l CallLog
	l.add("pat:a", CallRecord{})
	l.Forget("pat:a")
	if len(l.Recent("pat:a")) != 0 {
		t.Error("ring survived Forget")
	}
	tr := &callTrace{}
	tr.addParseError("parse error")
	if tr.rec.Items[0].Era != "" {
		t.Errorf("a body that did not parse has no era, got %q", tr.rec.Items[0].Era)
	}
}
