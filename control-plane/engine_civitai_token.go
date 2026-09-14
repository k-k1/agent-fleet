package main

// The operator's Civitai token, registered from the Console the same way as the Hugging Face
// token (engine_hf_token.go) and for the same reason: a Civitai account whose owner has accepted
// an uploader's terms, or paid for early access, is what turns a "login required" download into
// one the ingest task can fetch — and the CP has nowhere else to put that account's key.
//
// The two tokens are unrelated accounts on unrelated services, so this is its own secret and its
// own settings rows rather than a second value crammed into the Hugging Face ones: a deployment
// may register either, both or neither, and clearing one must never touch the other.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The settings rows. Deployment-wide, like the Hugging Face token: the account buys access to
// assets for the whole deployment's catalogue, not for one tenant's.
const (
	engineCivitaiTokenSetting    = "engine_civitai_token"
	engineCivitaiTokenRefSetting = "engine_civitai_token_key_ref"
	engineCivitaiTokenBySetting  = "engine_civitai_token_by"
	engineCivitaiTokenAtSetting  = "engine_civitai_token_at"
)

// engineCivitaiTokenKeyRef is the custodian key this deployment-wide value is sealed under —
// its own ref, not the Hugging Face token's, so the two can be rotated or cleared independently.
const engineCivitaiTokenKeyRef = "deployment-civitai"

// engineCivitaiTokenSentinel is what the stack creates the secret holding, and what a cleared
// token writes back. The fetch container reads it as "no token".
const engineCivitaiTokenSentinel = "-"

// engineCivitaiTokens is the registered token: the settings rows, the seal around them, and the
// secret it is carried through. Shares its two ports (engineSecretWriter, engineTokenSealer)
// with engineHfTokens rather than redeclaring them — both are already named for the operation,
// not for Hugging Face.
type engineCivitaiTokens struct {
	settings store.SettingsStore
	sealer   engineTokenSealer
	sm       engineSecretWriter
	// arn is the stack's secret. Empty on a stack that predates this feature, where there is
	// nothing to write to.
	arn string
}

// available reports whether this deployment can be given a token at all.
func (t *engineCivitaiTokens) available() bool {
	return t != nil && t.settings != nil && t.sm != nil && strings.TrimSpace(t.arn) != ""
}

// engineCivitaiTokenStatus is what the panel reads. It never carries the token: the CP cannot
// read the secret back, and a screen has no use for the value.
type engineCivitaiTokenStatus struct {
	Available  bool   `json:"available"`
	Configured bool   `json:"configured"`
	UpdatedBy  string `json:"updated_by,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// view is what the panel shows.
func (t *engineCivitaiTokens) view(ctx context.Context) engineCivitaiTokenStatus {
	if t == nil || !t.available() {
		return engineCivitaiTokenStatus{}
	}
	out := engineCivitaiTokenStatus{Available: true}
	enc, err := t.settings.GetSetting(ctx, engineCivitaiTokenSetting)
	if err != nil {
		log.Printf("engines: civitai token setting unreadable: %v", err)
		return out
	}
	if strings.TrimSpace(enc) == "" {
		return out
	}
	out.Configured = true
	if by, err := t.settings.GetSetting(ctx, engineCivitaiTokenBySetting); err == nil {
		out.UpdatedBy = by
	}
	if at, err := t.settings.GetSetting(ctx, engineCivitaiTokenAtSetting); err == nil {
		out.UpdatedAt = at
	}
	return out
}

// set seals the token, writes it down and carries it to the secret, in that order — see
// engineHfTokens.set for why the secret goes first.
func (t *engineCivitaiTokens) set(ctx context.Context, token, by string) *apiError {
	token = strings.TrimSpace(token)
	if token == "" {
		return &apiError{http.StatusBadRequest, errCodeCivitaiTokenEmpty, "the token is empty"}
	}
	if !t.available() {
		return t.unsupported()
	}
	if err := t.put(ctx, token); err != nil {
		return err
	}
	enc, ref, err := t.sealer.sealTenantSecret(ctx, engineCivitaiTokenKeyRef, token)
	if err != nil {
		return &apiError{http.StatusInternalServerError, errCodeCivitaiTokenStoreFailed, err.Error()}
	}
	if err := t.write(ctx, enc, ref, by); err != nil {
		return err
	}
	return nil
}

// clear forgets the token in both places.
func (t *engineCivitaiTokens) clear(ctx context.Context, by string) *apiError {
	if !t.available() {
		return t.unsupported()
	}
	if err := t.put(ctx, engineCivitaiTokenSentinel); err != nil {
		return err
	}
	return t.write(ctx, "", "", by)
}

// stage writes the stored token into the secret before an ingest that might need it. Run
// unconditionally, like the Hugging Face token's — the CP has no `GetSecretValue`, so nothing
// can be checked for staleness, and every ingest job carries whichever source it was given.
func (t *engineCivitaiTokens) stage(ctx context.Context) *apiError {
	if !t.available() {
		return nil
	}
	tok, err := t.plaintext(ctx)
	if err != nil {
		return err
	}
	if tok == "" {
		return nil
	}
	return t.put(ctx, tok)
}

// plaintext unseals the stored token.
func (t *engineCivitaiTokens) plaintext(ctx context.Context) (string, *apiError) {
	enc, err := t.settings.GetSetting(ctx, engineCivitaiTokenSetting)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeCivitaiTokenStoreFailed, err.Error()}
	}
	if strings.TrimSpace(enc) == "" {
		return "", nil
	}
	ref, err := t.settings.GetSetting(ctx, engineCivitaiTokenRefSetting)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeCivitaiTokenStoreFailed, err.Error()}
	}
	tok, err := t.sealer.openTenantSecret(ctx, enc, ref)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeCivitaiTokenStoreFailed,
			"the stored Civitai token could not be unsealed: " + err.Error()}
	}
	return strings.TrimSpace(tok), nil
}

func (t *engineCivitaiTokens) put(ctx context.Context, value string) *apiError {
	_, err := t.sm.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId: aws.String(t.arn), SecretString: aws.String(value),
	})
	if err != nil {
		return &apiError{http.StatusBadGateway, errCodeCivitaiTokenPutFailed,
			"could not write the token into the deployment's secret: " + err.Error()}
	}
	return nil
}

func (t *engineCivitaiTokens) write(ctx context.Context, enc, ref, by string) *apiError {
	rows := [][2]string{
		{engineCivitaiTokenSetting, enc},
		{engineCivitaiTokenRefSetting, ref},
		{engineCivitaiTokenBySetting, by},
		{engineCivitaiTokenAtSetting, store.NowTS()},
	}
	if enc == "" {
		rows[3][1] = ""
	}
	for _, kv := range rows {
		if err := t.settings.SetSetting(ctx, kv[0], kv[1]); err != nil {
			return &apiError{http.StatusInternalServerError, errCodeCivitaiTokenStoreFailed, err.Error()}
		}
	}
	return nil
}

func (t *engineCivitaiTokens) unsupported() *apiError {
	return &apiError{http.StatusConflict, errCodeCivitaiTokenUnsupported,
		"this deployment's engine stack has no secret to write the token into: update 60-engines"}
}

// newEngineCivitaiTokens wires the token onto an ingester. Nil-safe in every direction, like
// newEngineHfTokens.
func newEngineCivitaiTokens(def engineIngestDef, settings store.SettingsStore,
	sealer engineTokenSealer, sm engineSecretWriter) *engineCivitaiTokens {
	t := &engineCivitaiTokens{settings: settings, sealer: sealer, sm: sm,
		arn: strings.TrimSpace(def.CivitaiTokenSecret)}
	if sealer == nil {
		t.arn = ""
	}
	return t
}

// --- the admin routes ---------------------------------------------------------

func (a engineAdminAPI) civitaiTokens() *engineCivitaiTokens {
	if ing := a.reg.ingester(); ing != nil {
		return ing.civitaiTokens
	}
	return nil
}

// getCivitaiToken (GET /api/admin/engines/civitai-token) answers whether a token is registered,
// by whom and when — never the value.
func (a engineAdminAPI) getCivitaiToken(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.civitaiTokens().view(r.Context()))
}

// putCivitaiToken registers or replaces the token.
func (a engineAdminAPI) putCivitaiToken(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	t := a.civitaiTokens()
	if t == nil {
		writeAPIErr(w, (&engineCivitaiTokens{}).unsupported())
		return
	}
	var b struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&b); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineBadBody, "invalid JSON"})
		return
	}
	if apiErr := t.set(r.Context(), b.Token, ident.ID); apiErr != nil {
		writeAPIErr(w, apiErr)
		return
	}
	a.audit(r.Context(), ident, "engine.civitai_token", "register")
	log.Print("engines: Civitai token registered (sealed setting + stack secret)")
	writeJSON(w, http.StatusOK, t.view(r.Context()))
}

// deleteCivitaiToken forgets it.
func (a engineAdminAPI) deleteCivitaiToken(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	t := a.civitaiTokens()
	if t == nil {
		writeAPIErr(w, (&engineCivitaiTokens{}).unsupported())
		return
	}
	if apiErr := t.clear(r.Context(), ident.ID); apiErr != nil {
		writeAPIErr(w, apiErr)
		return
	}
	a.audit(r.Context(), ident, "engine.civitai_token", "remove")
	log.Print("engines: Civitai token removed (the stack secret holds the sentinel again)")
	writeJSON(w, http.StatusOK, t.view(r.Context()))
}
