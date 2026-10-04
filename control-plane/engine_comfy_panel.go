package main

// The LAN ComfyUI's URL and bearer, entered from the admin panel instead of AF_COMFY_URL /
// AF_COMFY_API_KEY (#957, ADR 0076 P1's first item and its 2026-10-04 addendum to decision 2).
//
// Precedence for the image role, highest first:
//
//  1. a MANAGED row of the engine table — the panel changes nothing and says so (409), for the
//     reason decision 2 gives the environment: replacing it would leave its ECS service running
//     with nobody to stop it;
//  2. the panel's URL;
//  3. AF_COMFY_URL;
//  4. an external or borrowed row.
//
// The key follows its URL's source: a panel URL presents the panel key or no bearer at all, never
// AF_COMFY_API_KEY or AF_ENGINE_API_KEY_IMAGE — those were configured for whatever host the
// environment names, and the panel URL may be a different one.
//
// Stored as one settings row holding the URL and the key sealed like the Hugging Face token
// (engine_hf_token.go): the deployment-wide custodian key, plaintext only on a deployment with no
// master key. The key is write-only — no route answers it, neither the audit log nor a log line
// carries it, and the panel row's requests do not follow a redirect off the URL's origin.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	// engineComfySetting holds the whole panel value as ONE JSON row, so the URL and the key that
	// belongs to it are written together or not at all. Separate rows could be left half-written
	// by a failed save — a new URL beside the old key — and the next boot would send that key to
	// a host it was never entered for.
	engineComfySetting = "engine_comfy_lan"
	// The deployment-wide custodian key, the one the Hugging Face token is sealed under: the value
	// belongs to no tenant.
	engineComfyKeyRef = engineHfTokenKeyRef
)

// engineComfyRecord is the stored panel value. KeyEnc is the sealed key ("" = no key) and KeyRef
// the custodian reference it was sealed under ("" = plaintext, a deployment with no master key).
type engineComfyRecord struct {
	URL    string `json:"url"`
	KeyEnc string `json:"key_enc,omitempty"`
	KeyRef string `json:"key_ref,omitempty"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
}

// engineComfyPanel is the stored panel value: the settings row and the seal around the key.
type engineComfyPanel struct {
	settings store.SettingsStore
	sealer   engineTokenSealer
}

// newEngineComfyPanel is nil on a CP with no store — nowhere to keep a value, so no panel.
func newEngineComfyPanel(mgr *manager) *engineComfyPanel {
	if mgr == nil || mgr.store == nil {
		return nil
	}
	return &engineComfyPanel{settings: mgr.store, sealer: mgr}
}

// load reads the stored value; the zero record when nothing is saved.
func (p *engineComfyPanel) load(ctx context.Context) (engineComfyRecord, error) {
	var rec engineComfyRecord
	raw, err := p.settings.GetSetting(ctx, engineComfySetting)
	if err != nil || strings.TrimSpace(raw) == "" {
		return rec, err
	}
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return engineComfyRecord{}, err
	}
	rec.URL = strings.TrimSpace(rec.URL)
	return rec, nil
}

// save writes the whole value in one row; the zero record clears it.
func (p *engineComfyPanel) save(ctx context.Context, rec engineComfyRecord) *apiError {
	raw := ""
	if rec.URL != "" {
		b, err := json.Marshal(rec)
		if err != nil {
			return &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()}
		}
		raw = string(b)
	}
	if err := p.settings.SetSetting(ctx, engineComfySetting, raw); err != nil {
		return &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()}
	}
	return nil
}

// boot reads the stored value for registry construction. A store that cannot answer is logged and
// the panel not applied; a key that cannot be unsealed is logged and the panel's URL served
// without a bearer (the upstream then answers 401, which names the problem) rather than quietly
// falling back to a different host from the environment.
func (p *engineComfyPanel) boot(ctx context.Context) (string, string) {
	if p == nil {
		return "", ""
	}
	rec, err := p.load(ctx)
	if err != nil {
		log.Printf("engines: the admin panel's ComfyUI setting is unreadable (%v) - not applied", err)
		return "", ""
	}
	if rec.URL == "" {
		return "", ""
	}
	key, aerr := p.key(ctx, rec)
	if aerr != nil {
		log.Printf("engines: the admin panel's ComfyUI key could not be read (%s) - %s is used without a bearer until it is entered again", aerr.message, rec.URL)
		return rec.URL, ""
	}
	return rec.URL, key
}

// key unseals the record's key. An unreadable value is an error, never an empty key, for the
// reason openTenantSecret gives.
func (p *engineComfyPanel) key(ctx context.Context, rec engineComfyRecord) (string, *apiError) {
	if strings.TrimSpace(rec.KeyEnc) == "" {
		return "", nil
	}
	k, err := p.sealer.openTenantSecret(ctx, rec.KeyEnc, rec.KeyRef)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed,
			"the stored ComfyUI key could not be unsealed: " + err.Error()}
	}
	return strings.TrimSpace(k), nil
}

// seal seals a plaintext key for a record.
func (p *engineComfyPanel) seal(ctx context.Context, key string) (string, string, *apiError) {
	enc, ref, err := p.sealer.sealTenantSecret(ctx, engineComfyKeyRef, key)
	if err != nil {
		return "", "", &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()}
	}
	return enc, ref, nil
}

// engineComfyURLValid normalises a URL typed into the panel, or refuses it. http and https only,
// a host, and no credentials, query or fragment: user:pass@ would put a secret where the panel,
// the admin list and every log line show the URL, and the bearer field is where one belongs.
func engineComfyURLValid(raw string) (string, *apiError) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", &apiError{http.StatusBadRequest, errCodeEngineComfyURLInvalid, "the URL is empty"}
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", &apiError{http.StatusBadRequest, errCodeEngineComfyURLInvalid, "the URL cannot be parsed"}
	}
	if u.User != nil {
		return "", &apiError{http.StatusBadRequest, errCodeEngineComfyURLCredentials,
			"the URL carries credentials; put the bearer in the key field instead"}
	}
	if sc := strings.ToLower(u.Scheme); sc != "http" && sc != "https" {
		return "", &apiError{http.StatusBadRequest, errCodeEngineComfyURLInvalid, "the URL must start with http:// or https://"}
	}
	if u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", &apiError{http.StatusBadRequest, errCodeEngineComfyURLInvalid,
			"the URL must be a scheme, a host and optionally a port and a path"}
	}
	return strings.TrimRight(s, "/"), nil
}

// applyComfyPanel makes the registry's image row what the panel now says: the panel's URL with
// its key, else the fallback this process booted with, else no image row at all. The row is
// REBUILT rather than edited, so the cached health answers (extWarm, extDown) that belonged to
// the previous URL go with it — a panel that just named a new host must not be told the old
// host's answer for ten more seconds.
//
// It reports false, and changes nothing, when a managed row holds the role.
func (r *engineRegistry) applyComfyPanel(u, key string) bool {
	if r == nil || r.buildComfy == nil {
		return false
	}
	r.comfyMu.Lock()
	defer r.comfyMu.Unlock()
	cur := r.get("image")
	if cur != nil && !cur.def.notManagedHere() {
		return false
	}
	var next *engineRuntimeState
	switch {
	case u != "":
		next = r.buildComfy(engineComfyRow(u, engineOriginPanel))
		if next != nil {
			next.apiKey = key
		}
	case r.comfyFallback != nil:
		next = r.buildComfy(*r.comfyFallback)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Read again under the lock that guards the swap: the table reloader can adopt a managed image
	// row (and start its controller) while buildComfy ran, and overwriting it would leave that
	// controller driving a GPU nothing in the registry can reach.
	if now := r.byKey["image"]; now != nil && !now.def.notManagedHere() {
		return false
	}
	if next != nil {
		r.byKey["image"] = next
		return true
	}
	// Cleared with nothing to fall back to. A borrowed row is left alone — the panel never put it
	// there — and anything else the panel or the environment synthesised goes.
	if now := r.byKey["image"]; now != nil && !now.def.remote() {
		delete(r.byKey, "image")
	}
	return true
}

// engineComfyManaged reports whether a managed table row holds the image role.
func (r *engineRegistry) engineComfyManaged() bool {
	e := r.get("image")
	return e != nil && !e.def.notManagedHere()
}

// engineComfyLanStatus is what the panel reads, as GET /api/admin/engines' `comfy_lan` and as the
// answer to a save. It never carries the key — not the value, its length or a prefix — only
// whether one is set.
type engineComfyLanStatus struct {
	// Available is false where the panel can change nothing: no store, or a managed table row
	// holds the image role (Source then says "table").
	Available bool `json:"available"`
	// Source is where the image row in effect comes from: "panel", "env", "table", "remote", or
	// "" when there is none.
	Source string `json:"source"`
	// URL is the image row in effect.
	URL         string `json:"url,omitempty"`
	PanelURL    string `json:"panel_url,omitempty"`
	PanelKeySet bool   `json:"panel_key_set"`
	// EnvURL / EnvKeySet are what the Control Plane's environment declares, shown so the panel
	// can say what clearing it falls back to.
	EnvURL    string `json:"env_url,omitempty"`
	EnvKeySet bool   `json:"env_key_set"`
	// FallbackSource / FallbackURL are what removing the panel value returns the image role to:
	// "env" (AF_COMFY_URL), "table" (an external row of the inline table) or "remote" (a
	// borrowed row the table declared), or "" for nothing — in which case RemoteConfigured says
	// whether a borrowed image engine may still arrive on the next catalogue poll.
	FallbackSource   string `json:"fallback_source,omitempty"`
	FallbackURL      string `json:"fallback_url,omitempty"`
	RemoteConfigured bool   `json:"remote_configured"`
	UpdatedBy        string `json:"updated_by,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

func (a engineAdminAPI) comfyPanel() *engineComfyPanel {
	if a.mgr == nil {
		return nil
	}
	return newEngineComfyPanel(a.mgr)
}

func (a engineAdminAPI) comfyLanStatus(ctx context.Context) engineComfyLanStatus {
	out := engineComfyLanStatus{
		EnvURL: strings.TrimSpace(envx.Or("AF_COMFY_URL", "")),
		EnvKeySet: strings.TrimSpace(envx.Or("AF_COMFY_API_KEY", "")) != "" ||
			strings.TrimSpace(envx.Or(engineAPIKeyEnvName("image"), "")) != "",
	}
	if e := a.reg.get("image"); e != nil {
		out.URL = e.def.URL
		switch {
		case !e.def.notManagedHere():
			out.Source = "table"
		case e.def.remote():
			out.Source = "remote"
		case e.def.origin == engineOriginPanel:
			out.Source = "panel"
		case e.def.origin == engineOriginEnv:
			out.Source = "env"
		default:
			out.Source = "table"
		}
	}
	if a.reg != nil && a.reg.comfyFallback != nil {
		fb := a.reg.comfyFallback
		out.FallbackURL = fb.URL
		switch {
		case fb.origin == engineOriginEnv:
			out.FallbackSource = "env"
		case fb.remote():
			out.FallbackSource = "remote"
		default:
			out.FallbackSource = "table"
		}
	}
	out.RemoteConfigured = strings.TrimSpace(envx.Or("AF_REMOTE_ENGINE_URL", "")) != ""
	p := a.comfyPanel()
	out.Available = p != nil && a.reg != nil && a.reg.buildComfy != nil && !a.reg.engineComfyManaged()
	if p == nil {
		return out
	}
	if rec, err := p.load(ctx); err == nil && rec.URL != "" {
		out.PanelURL, out.PanelKeySet = rec.URL, rec.KeyEnc != ""
		out.UpdatedBy, out.UpdatedAt = rec.By, rec.At
	}
	return out
}

// putComfyLan (PUT /api/admin/engines/comfy-lan) saves the URL and, optionally, the key:
// `{"url": "...", "key": "..."}` replaces the key, an absent or empty `key` keeps the stored one,
// and `"clear_key": true` removes it. The URL is validated before anything is sealed or written.
func (a engineAdminAPI) putComfyLan(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	var b struct {
		URL      string `json:"url"`
		Key      string `json:"key"`
		ClearKey bool   `json:"clear_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	u, aerr := engineComfyURLValid(b.URL)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	newKey := strings.TrimSpace(b.Key)
	if newKey != "" && b.ClearKey {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "key and clear_key together"})
		return
	}
	p, aerr := a.comfyWritable()
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	ctx := r.Context()
	// One save at a time, from reading the stored key to swapping the row: two saves interleaved
	// could leave the store saying one URL and the registry serving the other.
	a.reg.comfyWriteMu.Lock()
	defer a.reg.comfyWriteMu.Unlock()
	prev, err := p.load(ctx)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()})
		return
	}
	rec := engineComfyRecord{URL: u, By: ident.ID, At: store.NowTS()}
	key, keyAct := newKey, "set"
	switch {
	case b.ClearKey:
		key, keyAct = "", "cleared"
	case newKey == "":
		// The stored key stays with the record as it is sealed; unsealed only to hand to the row.
		if key, aerr = p.key(ctx, prev); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		rec.KeyEnc, rec.KeyRef, keyAct = prev.KeyEnc, prev.KeyRef, "unchanged"
	default:
		if rec.KeyEnc, rec.KeyRef, aerr = p.seal(ctx, newKey); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
	}
	if aerr := a.saveAndApply(ctx, p, prev, rec, key); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	// What changed, never the key: whether one was set, cleared or left alone.
	a.audit(ctx, ident, "engine.comfy_lan", "url="+u+" was="+engineComfyOr(prev.URL, "(none)")+" key="+keyAct)
	log.Printf("engines: image now comes from the admin panel (%s, key %s)", u, keyAct)
	writeJSON(w, http.StatusOK, a.comfyLanStatus(ctx))
}

// saveAndApply stores rec and swaps the registry's row to match. When the registry refuses —
// a managed row arrived between comfyWritable and the swap — the previous record is put back,
// so the store never holds a panel value the process is not serving, and the caller answers 409.
func (a engineAdminAPI) saveAndApply(ctx context.Context, p *engineComfyPanel, prev, rec engineComfyRecord, key string) *apiError {
	if aerr := p.save(ctx, rec); aerr != nil {
		return aerr
	}
	if !a.reg.applyComfyPanel(rec.URL, key) {
		if aerr := p.save(context.WithoutCancel(ctx), prev); aerr != nil {
			log.Printf("engines: a managed image row took the role during a panel save, and the previous panel value could not be restored: %s", aerr.message)
		}
		return engineComfyManagedErr()
	}
	go notifyEngineCatalogChanged(context.WithoutCancel(ctx), a.mgr, "image")
	return nil
}

// deleteComfyLan (DELETE /api/admin/engines/comfy-lan) forgets the panel's URL and key; the role
// falls back to AF_COMFY_URL, or goes away when there is none.
func (a engineAdminAPI) deleteComfyLan(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	p, aerr := a.comfyWritable()
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	ctx := r.Context()
	a.reg.comfyWriteMu.Lock()
	defer a.reg.comfyWriteMu.Unlock()
	prev, err := p.load(ctx)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()})
		return
	}
	if aerr := a.saveAndApply(ctx, p, prev, engineComfyRecord{}, ""); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.audit(ctx, ident, "engine.comfy_lan", "url=(none) was="+engineComfyOr(prev.URL, "(none)")+" key=cleared")
	log.Print("engines: the admin panel's ComfyUI URL was removed")
	writeJSON(w, http.StatusOK, a.comfyLanStatus(ctx))
}

// comfyWritable is the panel, or why it cannot be written.
func (a engineAdminAPI) comfyWritable() (*engineComfyPanel, *apiError) {
	p := a.comfyPanel()
	if p == nil || a.reg == nil || a.reg.buildComfy == nil {
		return nil, &apiError{http.StatusConflict, errCodeEngineComfyUnsupported,
			"this Control Plane has no store or no engine registry to keep a ComfyUI URL in"}
	}
	if a.reg.engineComfyManaged() {
		return nil, engineComfyManagedErr()
	}
	return p, nil
}

func engineComfyManagedErr() *apiError {
	return &apiError{http.StatusConflict, errCodeEngineComfyManaged,
		"the image role is a managed engine in this deployment's engine table, which wins over the panel; take the role out of the stack to point it at a LAN ComfyUI"}
}

func engineComfyOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// engineOriginPinKey marks an upstream request whose redirects must stay on the URL's own origin.
type engineOriginPinKey struct{}

// errEngineRedirectOffOrigin is the refusal engineCheckRedirect answers for such a request.
var errEngineRedirectOffOrigin = errors.New("the engine answered with a redirect to another origin, which is not followed for a URL set in the admin panel")

// engineDo sends one upstream request for eng. For the admin panel's row it pins redirects to the
// URL's origin: net/http copies Authorization onto a redirect to the same host name on another
// port (and onto a subdomain), so a 302 from the saved URL would hand the panel's key to a
// machine nobody saved. Other rows keep net/http's own redirect rules.
func engineDo(eng *engineRuntimeState, req *http.Request) (*http.Response, error) {
	if eng != nil && eng.def.origin == engineOriginPanel {
		req = req.WithContext(context.WithValue(req.Context(), engineOriginPinKey{}, true))
	}
	return engineClient.Do(req)
}

// engineCheckRedirect is engineClient's redirect policy: net/http's default (stop after 10), and
// for a pinned request no hop off the first request's scheme, host and effective port.
func engineCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if pinned, _ := req.Context().Value(engineOriginPinKey{}).(bool); pinned && !engineSameOrigin(req.URL, via[0].URL) {
		return errEngineRedirectOffOrigin
	}
	return nil
}

func engineSameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) &&
		engineEffectivePort(a) == engineEffectivePort(b)
}

func engineEffectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}
