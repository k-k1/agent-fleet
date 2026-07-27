package main

// MCP レジストリの REST 面（docs/48 P0 / ADR0031）。ユーザースコープの CRUD と接続テスト。
// テナント配布（CP 由来）と組み込み連携は読み取り専用で一覧に混ざり、無効化だけができる。
//
// 秘密（env / ヘッダの値）は決して外へ返さない: GET は常にマスクし、PUT がマスク値を
// そのまま返してきたら保存済みの値を維持する（connections の既存作法と同じ）。
//
// ⚠️ ここに足したパスは control-plane/routes.go にも登録が要る（CP は明示許可リスト方式）。

import (
	"context"
	"errors"
	"net/http"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
)

const (
	errCodeMCPNotFound = "mcp_not_found"
	errCodeMCPReadOnly = "mcp_read_only"
	errCodeMCPInvalid  = "mcp_invalid"
	errCodeMCPConflict = "mcp_name_taken"
)

// handleMCPServersGet returns the effective registry with every secret masked.
func handleMCPServersGet(w http.ResponseWriter, r *http.Request) {
	reg, err := mcpreg.Load()
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
		return
	}
	out := make([]mcpServerWire, 0, len(reg.Servers))
	for _, d := range reg.Servers {
		out = append(out, wireMCPServer(d))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"servers":         out,
		"tenantFetchedAt": reg.TenantFetchedAt,
		"shadowed":        reg.Shadowed,
	})
}

// mcpServerWire is the masked definition plus the derived flags the Console needs but
// must not compute itself (editability, "can this actually start").
type mcpServerWire struct {
	mcpreg.ServerDef
	Editable bool `json:"editable"`
	Ready    bool `json:"ready"`
}

func wireMCPServer(d mcpreg.ServerDef) mcpServerWire {
	return mcpServerWire{
		ServerDef: mcpreg.Masked(d),
		Editable:  d.Origin == mcpreg.OriginUser,
		Ready:     mcpreg.Ready(d),
	}
}

func handleMCPServerCreate(w http.ResponseWriter, r *http.Request) {
	var in mcpreg.ServerDef
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	out, err := mcpreg.Create(in)
	if err != nil {
		writeMCPErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wireMCPServer(out))
}

func handleMCPServerUpdate(w http.ResponseWriter, r *http.Request) {
	var in mcpreg.ServerDef
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	out, err := mcpreg.Update(r.PathValue("id"), in)
	if err != nil {
		writeMCPErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wireMCPServer(out))
}

func handleMCPServerDelete(w http.ResponseWriter, r *http.Request) {
	if err := mcpreg.Delete(r.PathValue("id")); err != nil {
		writeMCPErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("id")})
}

type mcpEnabledReq struct {
	Enabled bool `json:"enabled"`
}

// handleMCPServerEnabled is the one edit a member has over a tenant-distributed
// server: turn it off locally (docs/48 §4). For user rows it is just the flag.
func handleMCPServerEnabled(w http.ResponseWriter, r *http.Request) {
	var req mcpEnabledReq
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := mcpreg.SetEnabled(r.PathValue("id"), req.Enabled); err != nil {
		writeMCPErr(w, err)
		return
	}
	handleMCPServersGet(w, r)
}

// handleMCPServerTest runs the handshake (docs/48 §10). The body is a definition:
// with an id it is resolved against the stored one first, so masked secrets are
// filled in — which lets the Console test a saved server AND an unsaved edit through
// the same call.
//
// A stdio probe spawns the given command. That is not an escalation: this route is
// behind the CP↔Agent token and only the member's own workspace is reachable through
// it, and the member already has a terminal in that container (ADR0031 決定 2 — what
// must never be possible is a TENANT-distributed command, which the store refuses).
func handleMCPServerTest(w http.ResponseWriter, r *http.Request) {
	var in mcpreg.ServerDef
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if in.ID != "" {
		stored, err := mcpreg.Get(in.ID)
		if err != nil && !errors.Is(err, mcpreg.ErrNotFound) {
			httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
			return
		}
		if err == nil {
			if in.Transport == "" { // testing the stored definition as-is
				in = stored
			} else {
				in.Origin = stored.Origin
				in = mcpreg.MergeSecrets(in, stored)
			}
		}
	}
	if in.Origin == "" {
		in.Origin = mcpreg.OriginUser
	}
	// Enabled is irrelevant to a probe — the point is to test before turning it on.
	probe := in
	probe.Enabled = true
	if err := mcpreg.Validate(probe); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMCPInvalid, err.Error())
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	httpx.WriteJSON(w, http.StatusOK, mcpreg.Probe(ctx, probe))
}

func writeMCPErr(w http.ResponseWriter, err error) {
	var ve *mcpreg.ValidationError
	switch {
	case errors.Is(err, mcpreg.ErrNotFound):
		httpx.WriteErr(w, http.StatusNotFound, errCodeMCPNotFound, err.Error())
	case errors.Is(err, mcpreg.ErrReadOnly):
		httpx.WriteErr(w, http.StatusForbidden, errCodeMCPReadOnly, err.Error())
	case errors.Is(err, mcpreg.ErrNameTaken):
		httpx.WriteErr(w, http.StatusConflict, errCodeMCPConflict, err.Error())
	case errors.As(err, &ve):
		httpx.WriteErr(w, http.StatusBadRequest, errCodeMCPInvalid, err.Error())
	default:
		httpx.WriteErr(w, http.StatusInternalServerError, "store_failed", err.Error())
	}
}
