package mcpsrv

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const googleWorkspaceIssuer = "https://accounts.google.com"

type workspaceMCPService struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Host     string   `json:"host"`
	URL      string   `json:"url"`
	Preview  bool     `json:"preview,omitempty"`
	ReadOnly []string `json:"read_only"`
	Standard []string `json:"standard"`
}

// People and Classroom only serve v1. The other preset URLs use Google's toolset for hosts
// without form elicitation. Keep the scope union within the Agent's sign-in limit.
var workspaceMCPServices = []workspaceMCPService{
	workspaceService("gmail", "Gmail", "gmailmcp.googleapis.com", "v1self", false, "gmail.readonly", "gmail.compose"),
	workspaceService("drive", "Google Drive", "drivemcp.googleapis.com", "v1self", false, "drive.readonly", "drive.file"),
	workspaceService("docs", "Google Docs", "docsmcp.googleapis.com", "v1self", false, "documents.readonly drive.readonly", "documents drive.file"),
	workspaceService("sheets", "Google Sheets", "sheetsmcp.googleapis.com", "v1self", false, "spreadsheets.readonly drive.readonly", "spreadsheets drive.file"),
	workspaceService("slides", "Google Slides", "slidesmcp.googleapis.com", "v1self", false, "presentations.readonly drive.readonly", "presentations drive.file"),
	workspaceService("calendar", "Google Calendar", "calendarmcp.googleapis.com", "v1self", false, "calendar.events.readonly calendar.readonly", "calendar.events"),
	workspaceService("chat", "Google Chat", "chatmcp.googleapis.com", "v1self", false, "chat.messages.readonly chat.spaces.readonly chat.memberships.readonly", "chat.messages.create"),
	workspaceService("tasks", "Google Tasks", "tasksmcp.googleapis.com", "v1self", false, "tasks.readonly", ""),
	workspaceService("people", "People API", "people.googleapis.com", "v1", true, "contacts.readonly", ""),
	workspaceService("classroom", "Google Classroom", "classroommcp.googleapis.com", "v1", true, "classroom.courses.readonly", ""),
	workspaceService("search", "Universal Search", "workspacemcp.googleapis.com", "v1self", true,
		"gmail.readonly drive.readonly calendar.readonly chat.messages.readonly chat.spaces.readonly chat.memberships.readonly", ""),
}

func workspaceService(id, label, host, version string, preview bool, read, write string) workspaceMCPService {
	scopes := strings.Fields(strings.TrimSpace(read + " " + write))
	for i := range scopes {
		scopes[i] = "https://www.googleapis.com/auth/" + scopes[i]
	}
	return workspaceMCPService{
		ID: id, Name: "google-" + id, Label: label, Host: host, URL: "https://" + host + "/mcp/" + version, Preview: preview,
		ReadOnly: scopes[:len(strings.Fields(read))], Standard: scopes,
	}
}

type workspaceMCPPreset struct {
	RedirectURI string                `json:"redirect_uri,omitempty"`
	Services    []workspaceMCPService `json:"services"`
}

func workspacePreset(publicBase string) workspaceMCPPreset {
	p := workspaceMCPPreset{Services: workspaceMCPServices}
	if publicBase != "" {
		p.RedirectURI = strings.TrimRight(publicBase, "/") + "/oauth2/mcp/callback"
	}
	return p
}

type workspaceMCPRequest struct {
	TenantSlug   string   `json:"tenant_slug"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Services     []string `json:"services"`
	Bundle       string   `json:"bundle"`
}

func (b workspaceMCPRequest) definitions() ([]mcpServerBody, *APIError) {
	client := &mcpOAuthClientBody{
		Issuer: googleWorkspaceIssuer, ClientID: strings.TrimSpace(b.ClientID), ClientSecret: strings.TrimSpace(b.ClientSecret),
	}
	if client.ClientSecret == "" || client.ClientSecret == MaskedValue || !validOAuthClient(*client) {
		return nil, &APIError{http.StatusBadRequest, codeMCPOAuthClient, "a Google Workspace client needs a client id and secret"}
	}
	badSelection := &APIError{http.StatusBadRequest, "mcp_workspace_selection_invalid", "choose supported services and a scope bundle"}
	if len(b.Services) == 0 || (b.Bundle != "read_only" && b.Bundle != "standard") {
		return nil, badSelection
	}

	requested := make(map[string]bool, len(b.Services))
	for _, id := range b.Services {
		requested[id] = true
	}
	var defs []mcpServerBody
	var scopes []string
	seenScope := map[string]bool{}
	for _, service := range workspaceMCPServices {
		if !requested[service.ID] {
			continue
		}
		selectedScopes := service.ReadOnly
		if b.Bundle == "standard" {
			selectedScopes = service.Standard
		}
		for _, scope := range selectedScopes {
			if !seenScope[scope] {
				scopes = append(scopes, scope)
				seenScope[scope] = true
			}
		}
		defs = append(defs, mcpServerBody{
			Name: service.Name, Label: service.Label, URL: service.URL, Transport: "http",
			Targets: mcpTargets{Assistant: true, Session: true}, Enabled: true,
			Auth: mcpAuthOAuth, OAuthClient: client, Scopes: selectedScopes,
		})
	}
	if len(defs) != len(b.Services) {
		return nil, badSelection
	}
	if !validDefScopes(scopes) {
		return nil, &APIError{http.StatusBadRequest, codeMCPOAuthScopes, "the selected services exceed the shared sign-in scope limit"}
	}
	return defs, nil
}

// AdminCreateWorkspacePreset writes ordinary tenant definitions with one confidential client.
func (a ServerAPI) AdminCreateWorkspacePreset(w http.ResponseWriter, r *http.Request) {
	var b workspaceMCPRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
		writeAPIErr(w, &APIError{http.StatusBadRequest, "bad_body", "invalid JSON"})
		return
	}
	ident, tenant, ok := a.cp.TenantAdminFor(w, r, a.mcpTenantSlug(r, b.TenantSlug))
	if !ok {
		return
	}
	defs, aerr := b.definitions()
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	ctx := r.Context()
	var client store.MCPServerRow
	if aerr := a.resolveOAuthClient(ctx, tenant.ID, defs[0], nil, &client); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	now := store.NowTS()
	publicBase := a.cp.ServerIdentity(ctx).PublicBaseURL
	rows := make([]store.MCPServerRow, 0, len(defs))
	for _, def := range defs {
		if aerr := refuseOwnMCP(def.URL, publicBase); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		row := client
		row.ID, row.TenantID = store.NewID(), tenant.ID
		row.Name, row.Label, row.URL, row.Transport = def.Name, def.Label, def.URL, def.Transport
		row.Targets, row.Enabled, row.Auth, row.OAuthScopes = joinTargets(def.Targets), def.Enabled, def.Auth, joinScopes(def.Scopes)
		row.CreatedBy, row.CreatedAt, row.UpdatedAt = ident.Email, now, now
		rows = append(rows, row)
	}
	name, err := a.store.CreateMCPServers(ctx, tenant.ID, rows)
	if err != nil {
		writeAPIErr(w, mcpServerWriteError(err))
		return
	}
	if name != "" {
		writeAPIErr(w, mcpNameConflict(name))
		return
	}
	for _, row := range rows {
		a.recordMCPUpsert(ctx, ident, row, nil)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"created": len(rows)})
}
