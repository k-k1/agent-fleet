package main

// engine_remote_catalog.go — the far deployment's catalogue, read and mirrored (ADR 0079
// decision 7).
//
// The far side publishes /internal/engine/catalog for the AGENT to read, so the shape here is the
// Agent's shape and not this deployment's own store rows. Two consequences, and both are traps the
// ADR's review wrote down:
//
//   - ⚠️ `kind` IS NOT ON THE WIRE. LoRAs ride a separate `loras` array beside `model_rows`, so
//     which array a row arrived in is the only thing that says it is one. Get it wrong and every
//     borrowed LoRA becomes a model: it enters the launch menu, and a LoRA-only catalogue makes
//     hasModels answer true, which is a GPU started to run nothing.
//   - ⚠️ `enabled` is not on the wire either, because only enabled rows are published. Every
//     mirrored row is enabled by construction.
//
// 🔴 And the file key is `s3_key` here while store.EngineModelFile tags it `s3Key`. The two are
// different spellings of the same fact and the struct tag cannot be relied on, which is why the
// wire has its own type below. Only the BASENAME of that key is ever used — the Agent takes the
// last path segment as the file name to put in a loader node, because the box mirrors bucket keys
// onto disk verbatim — so mirroring the far key is correct rather than a leak of somebody else's
// bucket layout.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// engineRemoteHTTPTimeout bounds one catalogue or token call. Both are ordinary JSON round trips to
// another deployment's Control Plane — never to an engine — so there is no cold start to wait out
// here and a slow answer is a failure rather than something to hold a request for.
const engineRemoteHTTPTimeout = 20 * time.Second

// engineRemoteMaxBody bounds what is read from the far side. A catalogue is a list of model rows;
// anything of this size is a wrong URL answering, not a catalogue.
const engineRemoteMaxBody = 4 << 20

// engineRemoteCatalog is the far side's /internal/engine/catalog answer.
type engineRemoteCatalog struct {
	Engines []engineRemoteCatalogRow `json:"engines"`
}

// engineRemoteCatalogRow is one borrowed engine as the far deployment declares it. `api` and
// `provider` are the fields that must never be guessed (decision 2): the Workspace composes a
// completely different request for a comfy role than for an sdcpp one.
type engineRemoteCatalogRow struct {
	Key      string                     `json:"key"`
	API      string                     `json:"api"`
	Provider string                     `json:"provider"`
	BaseURL  string                     `json:"base_url"`
	Models   []engineRemoteCatalogModel `json:"model_rows"`
	Loras    []engineRemoteCatalogModel `json:"loras"`
	// NegativeAlways is what the far ADMINISTRATOR excludes from every image on that engine. It is
	// a deployment-wide setting there, so it is not a model row and does not go in the mirror; it
	// travels to the Workspace through this deployment's own catalogue answer instead.
	NegativeAlways string `json:"negative_always"`
}

// engineRemoteCatalogModel is one model row on the wire. Deliberately not store.EngineModel: see
// the file header for the three fields where the two shapes disagree.
type engineRemoteCatalogModel struct {
	ID              string                    `json:"id"`
	ContextTokens   int                       `json:"context_tokens"`
	MaxOutputTokens int                       `json:"max_output_tokens"`
	Description     string                    `json:"description"`
	Sizes           []string                  `json:"sizes"`
	BaseModel       string                    `json:"base_model"`
	Negative        string                    `json:"negative"`
	Params          *store.EngineParams       `json:"params"`
	Selected        bool                      `json:"selected"`
	Default         bool                      `json:"default"`
	Warm            bool                      `json:"warm"`
	Files           []engineRemoteCatalogFile `json:"files"`
}

// engineRemoteCatalogFile is one file of a model row, in the wire's own spelling.
type engineRemoteCatalogFile struct {
	Flag  string `json:"flag"`
	S3Key string `json:"s3_key"`
}

// fetchCatalog asks the far deployment which engines it offers. The issuing token is the only
// credential it needs, and this call reaches no engine at all — the whole point of the far side's
// catalogue route is that the launch menu can be drawn while every engine is asleep.
func (r *engineRemotes) fetchCatalog(ctx context.Context) ([]engineRemoteCatalogRow, error) {
	var out engineRemoteCatalog
	if err := r.call(ctx, http.MethodGet, "/internal/engine/catalog", nil, &out); err != nil {
		return nil, err
	}
	return out.Engines, nil
}

// call is the one HTTP shape both far-side calls share: the issuing token in, JSON out.
func (r *engineRemotes) call(ctx context.Context, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, engineRemoteHTTPTimeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := engineClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, engineRemoteMaxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		// The status, not the body: a 401 here means the borrowing membership is gone or the token
		// is wrong, and that is the sentence an operator needs. A far deployment's error body may
		// carry its own detail, so the first line of it rides along.
		return fmt.Errorf("%s %s answered %s: %s", method, path, resp.Status,
			strings.TrimSpace(tailLine(string(raw))))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// tailLine is the first line of a far error body, bounded. An error message is for a log line, and
// a whole HTML page in one is what makes logs unreadable.
func tailLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// applyCatalogRow brings one borrowed role up to date: the mirror is replaced, and the row is
// adopted if this process does not serve it yet.
//
// The row it adopts is built from the far declaration and nothing else — `api` and `provider` are
// the far administrator's to state, `url` is the far fleet's base, and `lifecycle` is what makes
// every other branch in the process treat it as somebody else's (decision 1).
func (r *engineRemotes) applyCatalogRow(ctx context.Context, reg *engineRegistry, row engineRemoteCatalogRow) {
	rem := r.forKey(row.Key)
	rem.setMirror(row)
	if e := reg.get(row.Key); e != nil {
		// Already served. The mirror above is the whole update: engineCatalog reads through it, so
		// a model enabled on the far side appears here within its own cache TTL.
		//
		// 🔴 A key a LOCAL row already holds is not borrowed. The registry is one map keyed by
		// role, and quietly replacing a managed row would leave its ECS service with nobody to stop
		// it — the same reasoning engineTableWithEnvRow already applies to AF_COMFY_URL.
		if !e.def.remote() {
			log.Printf("engines: %s is already served by a %s row, so it is not borrowed from %s",
				row.Key, lifecycleLabel(e.def), r.base)
		}
		return
	}
	def := engineDef{
		Key:       row.Key,
		API:       row.API,
		Provider:  row.Provider,
		URL:       r.base,
		Lifecycle: engineLifecycleRemote,
	}
	if reg.adopt(def) {
		log.Printf("engines: %s (%s/%s) is borrowed from %s (%d model(s))",
			row.Key, def.api(), def.Provider, r.base, len(row.Models))
		return
	}
	log.Printf("engines: %s is offered by %s but this process cannot take it on", row.Key, r.base)
}

// lifecycleLabel names a row's lifecycle for a log line, including the unnamed one.
func lifecycleLabel(d engineDef) string {
	if l := d.lifecycle(); l != "" {
		return l
	}
	return "managed"
}

// setMirror replaces this role's mirrored catalogue with what the far side just declared.
//
// 🔴 Always a list, never nil: engineCatalog.hasModels answers TRUE for a catalogue it cannot read
// at all, so an absent mirror would pass serve's no-models gate and then 404 every named model.
// An empty list is the honest "this role has nothing enabled", which serve already refuses with
// `engine_unavailable` and a sentence saying so.
func (e *engineRemote) setMirror(row engineRemoteCatalogRow) {
	if e == nil {
		return
	}
	rows := make([]store.EngineModel, 0, len(row.Models)+len(row.Loras))
	warm := ""
	for _, m := range row.Models {
		rows = append(rows, e.mirrorRow(m, ""))
		if m.Warm {
			warm = m.ID
		}
	}
	// The kind comes from WHICH ARRAY this arrived in and nowhere else — see the file header.
	for _, m := range row.Loras {
		rows = append(rows, e.mirrorRow(m, engineModelKindLora))
	}
	e.mu.Lock()
	e.rows, e.warmModel, e.loaded = rows, warm, true
	e.mu.Unlock()
}

// mirrorRow turns one wire row into a catalogue row of this deployment's own shape.
//
// Enabled is true by construction: the far side publishes only what its administrator switched on,
// so a row being here IS the enablement. `bytes` is absent on the wire, which leaves the panel's
// "+N s on the next cold start" at zero for a borrowed row — harmless, since that estimate is
// about a box this deployment does not pay for.
func (e *engineRemote) mirrorRow(m engineRemoteCatalogModel, kind string) store.EngineModel {
	files := make([]store.EngineModelFile, 0, len(m.Files))
	for _, f := range m.Files {
		files = append(files, store.EngineModelFile{Flag: f.Flag, S3Key: f.S3Key})
	}
	return store.EngineModel{
		Role:            e.key,
		ID:              m.ID,
		Kind:            kind,
		Files:           files,
		Enabled:         true,
		Selected:        m.Selected,
		Default:         m.Default,
		ContextTokens:   m.ContextTokens,
		MaxOutputTokens: m.MaxOutputTokens,
		Sizes:           m.Sizes,
		Params:          m.Params,
		Description:     m.Description,
		BaseModel:       m.BaseModel,
		NegativePrompt:  m.Negative,
	}
}
