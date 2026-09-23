package imagegen

// The studio routes (ADR 0100), registered now so the Control Plane's relay, the route goldens
// and the Console's client can be written against them. Every one answers 501 until the store
// behind it lands; the shapes are studio.go's.

import (
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

func studioNotYet(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteErr(w, http.StatusNotImplemented, "not_implemented",
		"the image studio is not implemented in this Agent yet (ADR 0100)")
}

// The handlers are separate names rather than one, so that the route table reads as the
// contract and each can be filled in on its own.
var (
	HandleStudios        = studioNotYet // GET list, POST create
	HandleStudio         = studioNotYet // GET, PUT (ImageStudioPatch, If-Match), DELETE
	HandleStudioBind     = studioNotYet // POST ImageStudioBind
	HandleStudioPress    = studioNotYet // POST ImageStudioPress
	HandleStudioRewind   = studioNotYet // POST ImageStudioRewind
	HandleStudioDraftLog = studioNotYet // GET DraftLogPage
	HandleStudioPersona  = studioNotYet // GET ImageStudioPersona
	HandleHistory        = studioNotYet // GET HistoryPage
	HandleKnowledge      = studioNotYet // GET Knowledge, POST KnowledgeAdd
)
