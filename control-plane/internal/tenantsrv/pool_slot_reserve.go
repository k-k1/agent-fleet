package tenantsrv

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// Reserving an EC2 slot for replacement at its workspace's next Start (#1473). The mark is a
// tag on the instance; the adapter re-reads everything from AWS and decides what may be
// reserved, as it does for TerminatePoolSlot. Every reservation, single or bulk, is its own
// intent-first audit pair, so "who reserved this box, and whose workspace did it move" has
// an answer per slot.

// maxBulkSlotReservations bounds one bulk request. It is far above any pool cap in use; it
// only keeps a malformed request from turning into thousands of AWS calls under one click.
const maxBulkSlotReservations = 500

// slotReserveBulkWire is the reply to POST /api/admin/ec2-pool/reserve-outdated.
type slotReserveBulkWire struct {
	Reserved []runtime.SlotReservation `json:"reserved"`
	Skipped  []slotReserveSkipWire     `json:"skipped"`
}

type slotReserveSkipWire struct {
	InstanceID string `json:"instance_id"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

// ReservePoolSlot (PUT /api/admin/ec2-pool/slots/{id}/replace) reserves one slot for
// replacement; DELETE on the same path takes the reservation back. super_admin only, like the
// rest of the pool screen.
func (a Admin) ReservePoolSlot(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "instance id required"})
		return
	}
	if !a.cp.HasSlotPool() {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_pool", "this runtime has no slot pool"})
		return
	}
	reserve := r.Method != http.MethodDelete
	res, aerr := a.reserveOne(w, r, ident, id, reserve, false)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if res == nil {
		return // beginIrreversible has answered
	}
	writeJSON(w, http.StatusOK, res)
}

// ReserveOutdatedPoolSlots (POST /api/admin/ec2-pool/reserve-outdated, body
// {"instance_ids": [...]}) reserves the slots the operator confirmed. The ids are the ones the
// confirmation listed, and each is re-checked against $Latest at the moment of the write: a
// slot that is no longer below it (or whose version cannot be read) is skipped and reported,
// never reserved, so the bulk action selects exactly the outdated slots whatever happened
// between the screen and the click.
func (a Admin) ReserveOutdatedPoolSlots(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	if !a.cp.HasSlotPool() {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_pool", "this runtime has no slot pool"})
		return
	}
	var body struct {
		InstanceIDs []string `json:"instance_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
		return
	}
	if len(body.InstanceIDs) == 0 {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "instance_ids required"})
		return
	}
	if len(body.InstanceIDs) > maxBulkSlotReservations {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request",
			fmt.Sprintf("at most %d instance_ids per request", maxBulkSlotReservations)})
		return
	}
	out := slotReserveBulkWire{Reserved: []runtime.SlotReservation{}, Skipped: []slotReserveSkipWire{}}
	seen := map[string]bool{}
	for _, raw := range body.InstanceIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		res, aerr := a.reserveOne(w, r, ident, id, true, true)
		if res == nil && aerr == nil {
			// The audit log could not take the intent, and beginIrreversible has answered
			// 503. Whatever was reserved before this slot stays reserved and audited.
			return
		}
		if aerr != nil {
			out.Skipped = append(out.Skipped, slotReserveSkipWire{InstanceID: id, Code: aerr.Code, Message: aerr.Message})
			continue
		}
		out.Reserved = append(out.Reserved, *res)
	}
	writeJSON(w, http.StatusOK, out)
}

// reserveOne writes the intent, asks the adapter, and records the outcome. It returns
// (nil, nil) when the intent could not be written — the 503 has then already been sent.
func (a Admin) reserveOne(w http.ResponseWriter, r *http.Request, ident store.Identity, id string, reserve, onlyOutdated bool) (*runtime.SlotReservation, *APIError) {
	action := "pool.slot_replace_reserve"
	if !reserve {
		action = "pool.slot_replace_cancel"
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: "", ActorKind: "admin", ActorID: ident.ID,
		Action: action, Target: id,
	})
	if !ok {
		return nil, nil
	}
	res, ok, err := a.cp.ReserveSlotReplacement(r.Context(), id, reserve, onlyOutdated)
	var aerr *APIError
	switch {
	case !ok:
		aerr = &APIError{http.StatusNotFound, "no_pool", "this runtime has no slot pool"}
	case errors.Is(err, runtime.ErrSlotNotFound):
		aerr = &APIError{http.StatusNotFound, "no_such_slot", err.Error()}
	case errors.Is(err, runtime.ErrSlotQuarantined):
		aerr = &APIError{http.StatusConflict, "slot_quarantined", err.Error()}
	case errors.Is(err, runtime.ErrSlotNotOutdated):
		aerr = &APIError{http.StatusConflict, "slot_not_outdated", err.Error()}
	case err != nil:
		aerr = internalErr(err)
	}
	if aerr != nil {
		in.Done(r.Context(), "error "+aerr.Code+": "+aerr.Message, aerr.Status)
		return nil, aerr
	}
	// The occupant is the person whose next Start this moves; the versions are why.
	in.Done(r.Context(), fmt.Sprintf("workspace=%s template=%s latest=%s",
		orDash(res.Workspace), orDash(res.TemplateVersion), orDash(res.TemplateLatest)), http.StatusOK)
	return &res, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
