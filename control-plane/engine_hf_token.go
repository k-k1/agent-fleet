package main

// The operator's Hugging Face token, registered from the Console instead of from a
// CloudFormation parameter (ADR 0072 decision 6 as revised, phase P5).
//
// Two places hold it and they are not equals. The SEALED SETTING is the record of truth: it
// survives a stack that is torn down and stood up again, and it is the value every ingest is
// staged from. The stack's Secrets Manager secret is a CARRYING PATH and nothing else —
// `secrets[].valueFrom` is the only way ECS gives a container a value without putting it in
// `environment`, which `DescribeTasks` returns in clear to anyone holding `ecs:DescribeTasks`
// (measured on the dev deployment, 2026-09-09).
//
// The CP can write that secret and cannot read it: 60-engines grants `PutSecretValue` on the
// one ARN and never `GetSecretValue`. So there is no way to ask whether the secret agrees with
// the setting, and the answer is not to ask — `stage` writes the value again before every
// ingest that needs it.

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

// The settings rows. Deployment-wide, like the egress mode: the token buys access to a
// repository for the whole deployment's catalogue, and ADR 0072 decision 6 keeps it that way
// on purpose — a per-tenant token would let tenant A's acceptance stage a model tenant B then
// uses, which is the thing decision 10 exists to prevent.
const (
	engineHfTokenSetting    = "engine_hf_token"
	engineHfTokenRefSetting = "engine_hf_token_key_ref"
	engineHfTokenBySetting  = "engine_hf_token_by"
	engineHfTokenAtSetting  = "engine_hf_token_at"
)

// engineHfTokenKeyRef is the custodian key this one deployment-wide value is sealed under.
// Everything else the custodian wraps is a tenant's, so it passes a tenant id; this is not,
// and a fixed ref says so rather than borrowing some tenant's key for a value that outlives
// that tenant.
const engineHfTokenKeyRef = "deployment"

// engineHfTokenSentinel is what the stack creates the secret holding, and what a cleared token
// writes back. The fetch container reads it as "no token" — an empty string is not available,
// because Secrets Manager refuses to store one.
const engineHfTokenSentinel = "-"

// engineSecretWriter is the narrow Secrets Manager port: one call, and deliberately not the
// one that reads.
type engineSecretWriter interface {
	PutSecretValue(context.Context, *secretsmanager.PutSecretValueInput,
		...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
}

// engineTokenSealer is the envelope the setting is stored in — the manager's, so this value
// is protected exactly like a tenant's IdP client secret, degrading to plaintext on a
// deployment with no master key the same way the rest of the CP does.
type engineTokenSealer interface {
	sealTenantSecret(ctx context.Context, keyRef, secret string) (enc, ref string, err error)
	openTenantSecret(ctx context.Context, enc, keyRef string) (string, error)
}

// engineHfTokens is the registered token: the settings rows, the seal around them, and the
// secret they are carried through.
type engineHfTokens struct {
	settings store.SettingsStore
	sealer   engineTokenSealer
	sm       engineSecretWriter
	// arn is the stack's secret. Empty on a stack that predates P5, where the token was a
	// CloudFormation parameter — there is nothing to write to, so nothing can be registered.
	arn string
	// legacy is what such a stack declared about itself. It is the whole answer there: no
	// registration is possible, and a gated repository either works or does not.
	legacy bool
}

// available reports whether this deployment can be given a token at all. False means the
// stack has not been updated, which is a sentence the panel has to be able to say — the
// alternative is a form that accepts a token and silently changes nothing.
func (t *engineHfTokens) available() bool {
	return t != nil && t.settings != nil && t.sm != nil && strings.TrimSpace(t.arn) != ""
}

// configured reports whether a gated repository can be taken in. On a pre-P5 stack that is
// what the table declared; otherwise it is a fact about the settings row.
func (t *engineHfTokens) configured(ctx context.Context) bool {
	if t == nil {
		return false
	}
	if !t.available() {
		return t.legacy
	}
	v, err := t.settings.GetSetting(ctx, engineHfTokenSetting)
	if err != nil {
		// A store that cannot answer is not a deployment without a token: saying "no token"
		// here would put "accept the licence on Hugging Face first" in front of somebody whose
		// token is fine. Assume the token is there and let the ingest fail with the real
		// reason.
		log.Printf("engines: hf token setting unreadable: %v", err)
		return true
	}
	return strings.TrimSpace(v) != ""
}

// engineHfTokenStatus is what the panel reads. A named type rather than a bare map: the map
// goldens can only pin what a map happens to hold today, and this shape is a contract the
// Console reads three fields out of.
//
// It never carries the token, its length or a prefix of it. The CP cannot read the secret
// back, and a screen has no use for the value.
type engineHfTokenStatus struct {
	// Available is whether this deployment can be given a token at all — false on a stack that
	// predates P5, which the panel has to be able to say. The alternative is a form that
	// accepts a token and silently changes nothing.
	Available bool `json:"available"`
	// Configured is whether a gated repository can be taken in.
	Configured bool `json:"configured"`
	// StackToken marks the pre-P5 deployment whose token came from CloudFormation: a working
	// deployment with nothing to register, which is not the same as a broken one.
	StackToken bool   `json:"stack_token,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// view is what the panel shows. Not `status`: the map goldens resolve a call by method name
// alone, and ttsAdminAPI.status would lend this site its key set.
func (t *engineHfTokens) view(ctx context.Context) engineHfTokenStatus {
	if t == nil {
		return engineHfTokenStatus{}
	}
	if !t.available() {
		return engineHfTokenStatus{Configured: t.legacy, StackToken: t.legacy}
	}
	out := engineHfTokenStatus{Available: true}
	enc, err := t.settings.GetSetting(ctx, engineHfTokenSetting)
	if err != nil {
		log.Printf("engines: hf token setting unreadable: %v", err)
		return out
	}
	if strings.TrimSpace(enc) == "" {
		return out
	}
	out.Configured = true
	if by, err := t.settings.GetSetting(ctx, engineHfTokenBySetting); err == nil {
		out.UpdatedBy = by
	}
	if at, err := t.settings.GetSetting(ctx, engineHfTokenAtSetting); err == nil {
		out.UpdatedAt = at
	}
	return out
}

// set seals the token, writes it down and carries it to the secret. The order is deliberate:
// the secret is written FIRST, because a setting saved against a `PutSecretValue` that will
// keep failing is a deployment whose panel says "registered" and whose every ingest is
// anonymous. A secret written and a setting that then fails to save is the harmless direction
// — the next successful registration overwrites it, and nothing reads the secret except an
// ingest this CP starts.
func (t *engineHfTokens) set(ctx context.Context, token, by string) *apiError {
	// Trimmed for the reason PARAMETERS-60-engines.md records: a trailing newline travels into
	// the Authorization header and comes back as a 401 that reads exactly like an unaccepted
	// licence.
	token = strings.TrimSpace(token)
	if token == "" {
		return &apiError{http.StatusBadRequest, errCodeHfTokenEmpty, "the token is empty"}
	}
	if !t.available() {
		return t.unsupported()
	}
	if err := t.put(ctx, token); err != nil {
		return err
	}
	enc, ref, err := t.sealer.sealTenantSecret(ctx, engineHfTokenKeyRef, token)
	if err != nil {
		return &apiError{http.StatusInternalServerError, errCodeHfTokenStoreFailed, err.Error()}
	}
	if err := t.write(ctx, enc, ref, by); err != nil {
		return err
	}
	return nil
}

// clear forgets the token in both places. The secret goes back to the sentinel rather than
// being left as it was: "removed" that leaves a working token in the path an ingest reads is
// the one outcome nobody would check for.
func (t *engineHfTokens) clear(ctx context.Context, by string) *apiError {
	if !t.available() {
		return t.unsupported()
	}
	if err := t.put(ctx, engineHfTokenSentinel); err != nil {
		return err
	}
	return t.write(ctx, "", "", by)
}

// stage writes the stored token into the secret before an ingest that needs it. It runs every
// time rather than when something looks stale, because nothing can look stale: the CP has no
// `GetSecretValue`, and a stack rebuilt under a registered token holds the sentinel with no
// way to notice.
func (t *engineHfTokens) stage(ctx context.Context) *apiError {
	if !t.available() {
		return nil // a pre-P5 stack carries the token itself
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

// plaintext unseals the stored token. An unreadable value is an ERROR, never an empty token,
// for the reason openTenantSecret gives: an anonymous fetch of a gated repository fails with a
// 401 that names the licence, and nobody traces that back to a key change.
func (t *engineHfTokens) plaintext(ctx context.Context) (string, *apiError) {
	enc, err := t.settings.GetSetting(ctx, engineHfTokenSetting)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeHfTokenStoreFailed, err.Error()}
	}
	if strings.TrimSpace(enc) == "" {
		return "", nil
	}
	ref, err := t.settings.GetSetting(ctx, engineHfTokenRefSetting)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeHfTokenStoreFailed, err.Error()}
	}
	tok, err := t.sealer.openTenantSecret(ctx, enc, ref)
	if err != nil {
		return "", &apiError{http.StatusInternalServerError, errCodeHfTokenStoreFailed,
			"the stored Hugging Face token could not be unsealed: " + err.Error()}
	}
	return strings.TrimSpace(tok), nil
}

func (t *engineHfTokens) put(ctx context.Context, value string) *apiError {
	_, err := t.sm.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId: aws.String(t.arn), SecretString: aws.String(value),
	})
	if err != nil {
		return &apiError{http.StatusBadGateway, errCodeHfTokenPutFailed,
			"could not write the token into the deployment's secret: " + err.Error()}
	}
	return nil
}

func (t *engineHfTokens) write(ctx context.Context, enc, ref, by string) *apiError {
	rows := [][2]string{
		{engineHfTokenSetting, enc},
		{engineHfTokenRefSetting, ref},
		{engineHfTokenBySetting, by},
		{engineHfTokenAtSetting, store.NowTS()},
	}
	if enc == "" {
		rows[3][1] = ""
	}
	for _, kv := range rows {
		if err := t.settings.SetSetting(ctx, kv[0], kv[1]); err != nil {
			return &apiError{http.StatusInternalServerError, errCodeHfTokenStoreFailed, err.Error()}
		}
	}
	return nil
}

func (t *engineHfTokens) unsupported() *apiError {
	return &apiError{http.StatusConflict, errCodeHfTokenUnsupported,
		"this deployment's engine stack has no secret to write the token into: update 60-engines"}
}

// newEngineHfTokens wires the token onto an ingester. Nil-safe in every direction: a
// deployment with no store, no AWS or a pre-P5 table still gets an object that answers
// `available() == false` rather than a nil check at each call site.
func newEngineHfTokens(def engineIngestDef, settings store.SettingsStore,
	sealer engineTokenSealer, sm engineSecretWriter) *engineHfTokens {
	t := &engineHfTokens{settings: settings, sealer: sealer, sm: sm,
		arn: strings.TrimSpace(def.TokenSecret), legacy: def.HasToken}
	if sealer == nil {
		t.arn = ""
	}
	return t
}

// --- the admin routes ---------------------------------------------------------

func (a engineAdminAPI) hfTokens() *engineHfTokens {
	if ing := a.reg.ingester(); ing != nil {
		return ing.tokens
	}
	return nil
}

// getHfToken (GET /api/admin/engines/hf-token) answers whether a token is registered, by whom
// and when — never the value. There is no route that returns it: the CP cannot read the secret
// and does not offer to unseal the setting for a screen.
func (a engineAdminAPI) getHfToken(w http.ResponseWriter, r *http.Request, _ store.Identity) {
	writeJSON(w, http.StatusOK, a.hfTokens().view(r.Context()))
}

// putHfToken registers or replaces the token.
func (a engineAdminAPI) putHfToken(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	t := a.hfTokens()
	if t == nil {
		writeAPIErr(w, (&engineHfTokens{}).unsupported())
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
	a.audit(r.Context(), ident, "engine.hf_token", "register")
	log.Print("engines: Hugging Face token registered (sealed setting + stack secret)")
	writeJSON(w, http.StatusOK, t.view(r.Context()))
}

// deleteHfToken forgets it.
func (a engineAdminAPI) deleteHfToken(w http.ResponseWriter, r *http.Request, ident store.Identity) {
	t := a.hfTokens()
	if t == nil {
		writeAPIErr(w, (&engineHfTokens{}).unsupported())
		return
	}
	if apiErr := t.clear(r.Context(), ident.ID); apiErr != nil {
		writeAPIErr(w, apiErr)
		return
	}
	a.audit(r.Context(), ident, "engine.hf_token", "remove")
	log.Print("engines: Hugging Face token removed (the stack secret holds the sentinel again)")
	writeJSON(w, http.StatusOK, t.view(r.Context()))
}
