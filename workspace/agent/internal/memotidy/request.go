package memotidy

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// The bounds of a request and of the operations a reply may hold. The Control Plane sends its own
// limits with every request, and the Agent refuses one that asks for more than these: the numbers
// live here once, and a request cannot widen what the Agent will parse.
const (
	maxItems        = 40
	maxTotalText    = 96 << 10
	maxRequestBytes = 512 << 10
	maxDestinations = 100
	maxIDBytes      = 200
	maxLabelBytes   = 200

	ceilMergeSources = 10
	ceilSplitParts   = 5
	ceilTextBytes    = 16 << 10
	ceilOperations   = 80
)

// Request is POST /memos/tidy.
type Request struct {
	Run          string        `json:"run"`
	Trigger      string        `json:"trigger"`
	Queue        Queue         `json:"queue"`
	Items        []Item        `json:"items"`
	Destinations []Destination `json:"destinations"`
	Limits       Limits        `json:"limits"`
}

// Queue names the queue being tidied.
type Queue struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Item is one pending message. Merge and Move say whether an operation of that kind may name
// it; Group is the set of items a merge may combine.
type Item struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	File  string `json:"file"`
	Group int    `json:"group"`
	Merge bool   `json:"merge"`
	Move  bool   `json:"move"`
}

// Destination is a category a message may be moved to.
type Destination struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Queue bool   `json:"queue"`
}

// Limits are the bounds the reply is checked against.
type Limits struct {
	MergeSources int `json:"mergeSources"`
	SplitParts   int `json:"splitParts"`
	TextBytes    int `json:"textBytes"`
	Operations   int `json:"operations"`
}

// decodeRequest reads and checks the body. Anything wrong is the one 400 code, with a message that
// names the field and never echoes its content.
func decodeRequest(r *http.Request) (*Request, string) {
	var req Request
	if e := httpx.DecodeStrictJSON(r, &req, maxRequestBytes); e != nil {
		return nil, e.Message
	}
	if msg := req.check(); msg != "" {
		return nil, msg
	}
	return &req, ""
}

func (q *Request) check() string {
	if !idOK(q.Run) {
		return "run must be an id"
	}
	if q.Trigger != "auto" {
		return `trigger must be "auto"`
	}
	if !idOK(q.Queue.ID) || !textOK(q.Queue.Label, maxLabelBytes) {
		return "queue needs an id and a label of at most 200 bytes"
	}
	if n := len(q.Items); n == 0 || n > maxItems {
		return fmt.Sprintf("items must hold 1 to %d items", maxItems)
	}
	seen := map[string]bool{}
	total := 0
	for i, it := range q.Items {
		if !idOK(it.ID) || seen[it.ID] {
			return fmt.Sprintf("items[%d].id must be a distinct id", i)
		}
		seen[it.ID] = true
		if it.Kind != "text" && it.Kind != "file" {
			return fmt.Sprintf("items[%d].kind must be text or file", i)
		}
		if !utf8.ValidString(it.Text) || !utf8.ValidString(it.File) {
			return fmt.Sprintf("items[%d] must be UTF-8", i)
		}
		if it.Group < 0 {
			return fmt.Sprintf("items[%d].group must not be negative", i)
		}
		total += len(it.Text) + len(it.File)
	}
	if total > maxTotalText {
		return fmt.Sprintf("the items hold more than %d bytes of text", maxTotalText)
	}
	if len(q.Destinations) > maxDestinations {
		return fmt.Sprintf("destinations holds more than %d entries", maxDestinations)
	}
	for i, d := range q.Destinations {
		if !idOK(d.ID) || !textOK(d.Name, maxLabelBytes) {
			return fmt.Sprintf("destinations[%d] needs an id and a name of at most 200 bytes", i)
		}
	}
	l := q.Limits
	switch {
	case l.MergeSources < 2 || l.MergeSources > ceilMergeSources:
		return fmt.Sprintf("limits.mergeSources must be 2 to %d", ceilMergeSources)
	case l.SplitParts < 2 || l.SplitParts > ceilSplitParts:
		return fmt.Sprintf("limits.splitParts must be 2 to %d", ceilSplitParts)
	case l.TextBytes < 1 || l.TextBytes > ceilTextBytes:
		return fmt.Sprintf("limits.textBytes must be 1 to %d", ceilTextBytes)
	case l.Operations < 1 || l.Operations > ceilOperations:
		return fmt.Sprintf("limits.operations must be 1 to %d", ceilOperations)
	}
	return ""
}

// idOK is a non-empty id of bounded length with no control character: it is quoted in the JSON
// the model reads and in the log, so nothing else about its shape matters here.
func idOK(s string) bool {
	return textOK(s, maxIDBytes) && s != "" && !strings.ContainsFunc(s, unicode.IsSpace)
}

func textOK(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
