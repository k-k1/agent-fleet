package tenantsrv

import (
	"context"
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
	detail := fmt.Sprintf("workspace=%s template=%s latest=%s",
		orDash(res.Workspace), orDash(res.TemplateVersion), orDash(res.TemplateLatest))
	if len(res.Reasons) > 0 {
		detail += " reasons=" + strings.Join(res.Reasons, ",")
	}
	in.Done(r.Context(), detail, http.StatusOK)
	return &res, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ForceStopPoolSlot (POST /api/admin/ec2-pool/slots/{id}/force-stop, optional body
// {"override_healthy": true}) stops a wedged slot (#2449). super_admin
// only: slots are shared across tenants, so no tenant_admin owns one.
//
// The adapter decides, from AWS, what may be stopped. Its refusals are answers rather than
// faults: an id that is not a slot of this pool (404), a slot that is already stopped (409),
// a quarantined slot (409 slot_quarantined), and a slot whose agents both answer (409 slot_healthy — the Console sets the
// override only after the operator confirms the agents still answer). Whatever the adapter managed before failing is
// kept in the audit line, since the occupant's Start may be ended with the stop still unsent.
//
// Once the adapter has taken its ownership token the work runs on the CP's own context, and the
// request only bounds the wait: when the client goes away or the time runs out first, the answer
// is 202 with in_progress=true and the audit outcome is written when the work ends.
func (a Admin) ForceStopPoolSlot(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "instance id required"})
		return
	}
	if !a.cp.HasSlotPool() {
		writeAPIErr(w, &APIError{http.StatusNotFound, "no_pool", "this runtime has no slot pool"})
		return
	}
	var body struct {
		OverrideHealthy bool `json:"override_healthy"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_request", "invalid JSON body"})
			return
		}
	}
	in, ok := a.beginIrreversible(w, r, store.AuditLog{
		TenantID: "", ActorKind: "admin", ActorID: ident.ID,
		Action: "pool.slot_force_stop", Target: id,
	})
	if !ok {
		return
	}
	// finish records the outcome. It runs on the handler's thread, or, when the request ended
	// after the force-stop had taken its token, from the runtime once the work is done.
	finish := func(ctx context.Context, res runtime.SlotForceStop, err error) *APIError {
		detail := fmt.Sprintf("workspace=%s state=%s agent_connected=%t ssm_online=%t override=%t predicate=%s cancelled=%d cancel_failed=%d forced=%t deregistered=%t",
			orDash(res.Workspace), orDash(res.PreviousState), res.AgentConnected, res.SSMOnline, body.OverrideHealthy, orDash(res.Predicate),
			res.CancelledCommands, res.CancelFailed, res.Forced, res.Deregistered)
		aerr := forceStopAPIError(err)
		if aerr != nil {
			in.Done(ctx, "error "+aerr.Code+": "+aerr.Message+" ("+detail+")", aerr.Status)
			return aerr
		}
		in.Done(ctx, detail, http.StatusOK)
		return nil
	}
	ctx := runtime.WithForceStopLate(r.Context(), func(res runtime.SlotForceStop, err error) {
		// The request is gone; this is the outcome the 202 promised.
		finish(context.Background(), res, err)
	})
	res, ok, err := a.cp.ForceStopSlot(ctx, id, body.OverrideHealthy)
	if !ok {
		refuseIrreversible(w, r, in, &APIError{http.StatusNotFound, "no_pool", "this runtime has no slot pool"})
		return
	}
	if errors.Is(err, runtime.ErrSlotForceStopInProgress) {
		// No in.Done: the late callback writes the single outcome row when the work ends.
		writeJSON(w, http.StatusAccepted, struct {
			runtime.SlotForceStop
			InProgress bool `json:"in_progress"`
		}{res, true})
		return
	}
	if aerr := finish(r.Context(), res, err); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// forceStopAPIError maps the adapter's answers onto the API's; nil for success.
func forceStopAPIError(err error) *APIError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, runtime.ErrSlotNotFound):
		return &APIError{http.StatusNotFound, "no_such_slot", err.Error()}
	case errors.Is(err, runtime.ErrSlotNotRunning):
		return &APIError{http.StatusConflict, "slot_not_running", err.Error()}
	case errors.Is(err, runtime.ErrSlotQuarantined):
		return &APIError{http.StatusConflict, "slot_quarantined", err.Error()}
	case errors.Is(err, runtime.ErrSlotTaskENIHeld):
		return &APIError{http.StatusConflict, "slot_task_eni_held", err.Error()}
	case errors.Is(err, runtime.ErrSlotStartedAgain):
		return &APIError{http.StatusConflict, "slot_started_again", err.Error()}
	case errors.Is(err, runtime.ErrSlotHealthy):
		return &APIError{http.StatusConflict, "slot_healthy", err.Error()}
	case errors.Is(err, runtime.ErrSlotHealthUnknown):
		return &APIError{http.StatusConflict, "slot_health_unknown", err.Error()}
	case errors.Is(err, runtime.ErrSlotRescueActive):
		return &APIError{http.StatusConflict, "slot_rescue_active", err.Error()}
	default:
		return internalErr(err)
	}
}
