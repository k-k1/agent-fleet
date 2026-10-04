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
// Stored as sealed settings rows, exactly like the Hugging Face token (engine_hf_token.go): the
// deployment-wide custodian key, plaintext only on a deployment with no master key. The key is
// write-only — no route answers it, and neither the audit log nor a log line carries it.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

const (
	engineComfyURLSetting    = "engine_comfy_lan_url"
	engineComfyKeySetting    = "engine_comfy_lan_key"
	engineComfyKeyRefSetting = "engine_comfy_lan_key_ref"
	engineComfyBySetting     = "engine_comfy_lan_by"
	engineComfyAtSetting     = "engine_comfy_lan_at"
	// The deployment-wide custodian key, the one the Hugging Face token is sealed under: the value
	// belongs to no tenant.
	engineComfyKeyRef = engineHfTokenKeyRef
)

// engineComfyPanel is the stored panel value: the settings rows and the seal around the key.
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

// boot reads the stored value for registry construction. A store that cannot answer, or a key
// that cannot be unsealed, is logged rather than fatal: the CP serves the panel's URL without a
// bearer (the upstream then answers 401, which names the problem) instead of quietly falling
// back to a different host from the environment.
func (p *engineComfyPanel) boot(ctx context.Context) (string, string) {
	if p == nil {
		return "", ""
	}
	u, err := p.settings.GetSetting(ctx, engineComfyURLSetting)
	if err != nil {
		log.Printf("engines: the admin panel's ComfyUI URL is unreadable (%v) - not applied", err)
		return "", ""
	}
	u = strings.TrimSpace(u)
	if u == "" {
		return "", ""
	}
	key, aerr := p.key(ctx)
	if aerr != nil {
		log.Printf("engines: the admin panel's ComfyUI key could not be read (%s) - %s is used without a bearer until it is entered again", aerr.message, u)
		return u, ""
	}
	return u, key
}

// storedURL is the panel's URL, "" when none is saved.
func (p *engineComfyPanel) storedURL(ctx context.Context) (string, error) {
	v, err := p.settings.GetSetting(ctx, engineComfyURLSetting)
	return strings.TrimSpace(v), err
}

// key unseals the stored key. An unreadable value is an error, never an empty key, for the
// reason openTenantSecret gives.
func (p *engineComfyPanel) key(ctx context.Context) (string, *apiError) {
	enc, err := p.settings.GetSetting(ctx, engineComfyKeySetting)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()}
	}
	if strings.TrimSpace(enc) == "" {
		return "", nil
	}
	ref, err := p.settings.GetSetting(ctx, engineComfyKeyRefSetting)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()}
	}
	k, err := p.sealer.openTenantSecret(ctx, enc, ref)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed,
			"the stored ComfyUI key could not be unsealed: " + err.Error()}
	}
	return strings.TrimSpace(k), nil
}

// write saves URL and key together. key is the plaintext to seal ("" = no key).
func (p *engineComfyPanel) write(ctx context.Context, u, key, by string) *apiError {
	enc, ref := "", ""
	if key != "" {
		var err error
		if enc, ref, err = p.sealer.sealTenantSecret(ctx, engineComfyKeyRef, key); err != nil {
			return &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()}
		}
	}
	at := store.NowTS()
	if u == "" {
		at = ""
	}
	for _, kv := range [][2]string{
		{engineComfyURLSetting, u},
		{engineComfyKeySetting, enc},
		{engineComfyKeyRefSetting, ref},
		{engineComfyBySetting, by},
		{engineComfyAtSetting, at},
	} {
		if err := p.settings.SetSetting(ctx, kv[0], kv[1]); err != nil {
			return &apiError{http.StatusInternalServerError, errCodeEngineComfyStoreFailed, err.Error()}
		}
	}
	return nil
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
	if next != nil {
		r.byKey["image"] = next
		return true
	}
	// Cleared with nothing to fall back to. A borrowed row is left alone — the panel never put it
	// there — and anything else the panel or the environment synthesised goes.
	if cur != nil && !cur.def.remote() {
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
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
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
	p := a.comfyPanel()
	out.Available = p != nil && a.reg != nil && a.reg.buildComfy != nil && !a.reg.engineComfyManaged()
	if p == nil {
		return out
	}
	if u, err := p.storedURL(ctx); err == nil {
		out.PanelURL = u
	}
	if enc, err := p.settings.GetSetting(ctx, engineComfyKeySetting); err == nil {
		out.PanelKeySet = strings.TrimSpace(enc) != ""
	}
	if out.PanelURL != "" {
		out.UpdatedBy, _ = p.settings.GetSetting(ctx, engineComfyBySetting)
		out.UpdatedAt, _ = p.settings.GetSetting(ctx, engineComfyAtSetting)
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
	prevURL, _ := p.storedURL(ctx)
	key, keyAct := newKey, "set"
	switch {
	case b.ClearKey:
		key, keyAct = "", "cleared"
	case newKey == "":
		if key, aerr = p.key(ctx); aerr != nil {
			writeAPIErr(w, aerr)
			return
		}
		keyAct = "unchanged"
	}
	if aerr := p.write(ctx, u, key, ident.ID); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.reg.applyComfyPanel(u, key)
	go notifyEngineCatalogChanged(context.WithoutCancel(ctx), a.mgr, "image")
	// What changed, never the key: whether one was set, cleared or left alone.
	a.audit(ctx, ident, "engine.comfy_lan", "url="+u+" was="+engineComfyOr(prevURL, "(none)")+" key="+keyAct)
	log.Printf("engines: image now comes from the admin panel (%s, key %s)", u, keyAct)
	writeJSON(w, http.StatusOK, a.comfyLanStatus(ctx))
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
	prevURL, _ := p.storedURL(ctx)
	if aerr := p.write(ctx, "", "", ident.ID); aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	a.reg.applyComfyPanel("", "")
	go notifyEngineCatalogChanged(context.WithoutCancel(ctx), a.mgr, "image")
	a.audit(ctx, ident, "engine.comfy_lan", "url=(none) was="+engineComfyOr(prevURL, "(none)")+" key=cleared")
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
		return nil, &apiError{http.StatusConflict, errCodeEngineComfyManaged,
			"the image role is a managed engine in this deployment's engine table, which wins over the panel; take the role out of the stack to point it at a LAN ComfyUI"}
	}
	return p, nil
}

func engineComfyOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
