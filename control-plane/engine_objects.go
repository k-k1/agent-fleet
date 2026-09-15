package main

// engine_objects.go — the bucket read as the ledger (ADR 0085 decisions 2, 3 and 7).
//
// 🔴 Why this exists, in one measurement. On 2026-09-15 af-sandbox held about 24 GB under
// `image/` that the panel could not show at all: the rows had been forgotten, the jobs
// dismissed, and `GET …/storage` "returns only keys the server already knows" — a HeadObject
// fan-out over the keys rows and jobs name. Forget both and the object goes on costing money
// while ceasing to exist for every screen. The permission to look was already there: the CP task
// role holds `s3:ListBucket` conditioned on the `llm/*` and `image/*` prefixes
// (60-engines.yaml), and the comment beside it said "the CP has no list operation" — a statement
// about the code, not about the grant.
//
// So: one route lists the role's prefix and joins every object with what the database says about
// it. The two-layer truth of ADR 0072 decision 2 is unchanged and is what this builds on — S3
// says what exists, the database says what is offered. The ledger lists; it never offers. Nothing
// here enables a row.
//
// The acts are on the MODEL, never on a part (decision 3). The one object-side act is
// `register`, and it is still an act on a model: it creates the checkpoint row that misplaced
// bytes belong to and then runs `complete` (engine_complete.go) on it.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// The placement verdict. Two values and not three: "the ledger has nothing to say about where
// this belongs" is carried by `role_dir: other`, not by a third placement word, because the
// Console draws placement as a repair button and a third state would be a button that does
// nothing.
const (
	engineObjectPlacementOK        = "ok"
	engineObjectPlacementMisplaced = "misplaced"
)

// The object's state. `present` / `missing` are the bucket's answer; `uploading` and `failed` are
// a job's (decision 6 — a job is its destination object's progress, not a second list).
const (
	engineObjectUploading = "uploading"
	engineObjectFailed    = "failed"
	// engineObjectDeleting is the only one of the four that no database row is behind: the CP has
	// no s3:DeleteObject, the ingest task does the deleting, and that task writes no job row. It
	// is held in memory by the storage view (engineStorage.markDeleting) and bounded by a TTL,
	// because nothing ever comes back to say the task finished.
	engineObjectDeleting = "deleting"
)

// engineObjectRoleDirOther is every key the loader-directory table has no opinion about: a file
// at the role's own root, a directory nothing enumerates, and anything whose name is not a model
// file at all. 🔴 Those are LISTED rather than hidden — a `.json` nobody declares is still bytes
// in the bucket, and a ledger that quietly dropped it would be the fault this file exists to end.
const engineObjectRoleDirOther = "other"

// engineObjectRoleDirs is engineComfyRoleDir read backwards: the directories a ComfyUI loader
// enumerates, and the flag each one is the home of. `text_encoders` maps to `--clip_l` because
// all three encoders share that directory (TripleCLIPLoader enumerates it) — which flag of the
// three a file plays is a question the ledger cannot answer from a path, and does not try to.
var engineObjectRoleDirs = map[string]string{
	"checkpoints":      "",
	"diffusion_models": "--diffusion-model",
	"text_encoders":    "--clip_l",
	"vae":              "--vae",
	"loras":            "",
}

// engineModelFileExts are the names a loader could load. Taken from the same list the Console's
// id proposal strips (`engineIdFromFile`), so the two agree on what a model file is called.
var engineModelFileExts = []string{".safetensors", ".gguf", ".ckpt", ".pt", ".sft", ".bin"}

func engineModelFileName(key string) bool {
	lower := strings.ToLower(key)
	for _, ext := range engineModelFileExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// engineObjectRoleDir classifies one key by its FIRST path segment under the role.
//
// 🔴 The first segment and not "does the key contain `vae/` anywhere": `image/checkpoints/
// split_files/vae/x.safetensors` is a checkpoints object staged two directories too deep, and
// calling it a VAE would put the repair button on the wrong directory. The depth is what
// `placement` then reports.
//
// `llm` needs no special case and gets none (ADR 0085 open question 3): its shards live under
// `llm/<model name>/`, whose first segment is not in the table, so they answer `other` and
// `placement: ok` — which is exactly true. Nothing in this ADR changes llm behaviour.
func engineObjectRoleDir(role, key string) string {
	rest := strings.TrimPrefix(key, strings.TrimSpace(role)+"/")
	if rest == key || !engineModelFileName(rest) {
		return engineObjectRoleDirOther
	}
	i := strings.Index(rest, "/")
	if i <= 0 {
		return engineObjectRoleDirOther
	}
	if _, ok := engineObjectRoleDirs[rest[:i]]; ok {
		return rest[:i]
	}
	return engineObjectRoleDirOther
}

// engineObjectPlacement answers whether the key is the one `engineComfyKeyFor` would compose for
// a file of that directory: `<role>/<dir>/<base name>`, flat. Anything deeper is `misplaced`,
// which is the single fact both of af-sandbox's two failure shapes reduce to —
// `image/checkpoints/…` for a split family's weights, and `image/diffusion_models/split_files/…`
// with the role chosen correctly.
func engineObjectPlacement(role, key, roleDir string) string {
	if roleDir == engineObjectRoleDirOther {
		return engineObjectPlacementOK
	}
	if strings.TrimSpace(role)+"/"+roleDir+"/"+path.Base(key) == key {
		return engineObjectPlacementOK
	}
	return engineObjectPlacementMisplaced
}

// engineObjectDeclaredBy is one row that names this key, and the role it reads it as. Both,
// because a key shared by two rows under two flags is normal (`text_encoders/` has been shared
// since ADR 0072 P2) and "who reads it, as what" is what makes a deletion refusal actionable.
type engineObjectDeclaredBy struct {
	ModelID string `json:"model_id"`
	Flag    string `json:"flag"`
}

// engineObjectJob is the progress an unfinished job lends its destination object. A `done` job is
// NOT here: what it leaves behind is provenance on the object (`source`, `artifact_identity`),
// and a finished download is not something anybody can act on.
type engineObjectJob struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
	CreatedAt string `json:"created_at"`
}

// engineObjectRow is one line of the ledger.
type engineObjectRow struct {
	Key          string `json:"key"`
	Bytes        int64  `json:"bytes"`
	LastModified string `json:"last_modified,omitempty"`
	RoleDir      string `json:"role_dir"`
	Placement    string `json:"placement"`
	State        string `json:"state"`
	// DeclaredBy is never omitted. An empty list is the ledger's most important answer — it is
	// what "nobody reads these bytes" looks like — and a key the Console has to treat as absent
	// would read as "not sent yet".
	DeclaredBy       []engineObjectDeclaredBy `json:"declared_by"`
	Source           string                   `json:"source,omitempty"`
	ArtifactIdentity string                   `json:"artifact_identity,omitempty"`
	License          string                   `json:"license,omitempty"`
	Job              *engineObjectJob         `json:"job,omitempty"`

	// tenants is who may see this row, and it stays off the wire: the visibility rule is the
	// job list's (engine_ingest_perm.go) and a granted tenant learning which OTHER tenant put
	// an object there is not part of it.
	tenants map[string]struct{} `json:"-"`
}

type engineObjectsResponse struct {
	Objects   []engineObjectRow `json:"objects"`
	CheckedAt string            `json:"checked_at"`
}

// engineLedger is the joined table, kept addressable by key because both acts on it
// (`register`, `complete`) ask "what does the bucket hold for this one role".
type engineLedger struct {
	role      string
	objects   []engineObjectRow
	byKey     map[string]*engineObjectRow
	checkedAt string
}

func (l *engineLedger) at(key string) *engineObjectRow {
	if l == nil || l.byKey == nil {
		return nil
	}
	return l.byKey[strings.TrimSpace(key)]
}

// present answers the one question a plan may act on: these bytes are in the bucket now and no
// job is about to replace them.
//
// 🔴 A FAILED job on the key is not a reason to say no (PR #691's rule, which ADR 0085 decision 6
// makes the definition of a holder: an object, or a task that could have written one). On
// af-sandbox three failed attempts at a repair fenced off the very repair they were attempting,
// and the operator had no way to see why.
func (l *engineLedger) present(key string) *engineObjectRow {
	row := l.at(key)
	if row == nil || row.State != engineStoragePresent || l.inFlight(row) {
		return nil
	}
	return row
}

// inFlight is "a task is doing something to these bytes right now", which is the only condition
// that fences an object off: an upload that may replace them, or a deletion that may remove them.
// A FAILED job is not one (PR #691's rule) — it is a line with a dismiss button, not a lock.
func (l *engineLedger) inFlight(row *engineObjectRow) bool {
	if row == nil || row.Job == nil {
		return false
	}
	return row.Job.State == engineObjectUploading || row.Job.State == engineObjectDeleting
}

// inDir is every present object of one loader directory, in key order. The candidate pool a
// missing role is filled from (decision 3).
func (l *engineLedger) inDir(dir string) []*engineObjectRow {
	var out []*engineObjectRow
	for i := range l.objects {
		if l.objects[i].RoleDir == dir && l.objects[i].State == engineStoragePresent && !l.inFlight(&l.objects[i]) {
			out = append(out, &l.objects[i])
		}
	}
	return out
}

// sameBaseName is how a file already in the bucket under the WRONG key is recognised as the one a
// part list names. 🔴 The base name and not the artifact identity: the object whose identity
// nothing recorded is exactly the one this road exists for — af-sandbox's parts were at both the
// wrong and the right keys with their jobs dismissed. The move it earns costs no download and is
// reversible; declaring it is what the operator confirms by pressing.
func (l *engineLedger) sameBaseName(name, dir string) []*engineObjectRow {
	var out []*engineObjectRow
	for _, row := range l.inDir(dir) {
		if path.Base(row.Key) == name {
			out = append(out, row)
		}
	}
	return out
}

// engineLedgerFor lists the role's prefix and joins it with the catalogue and the job history.
//
// Every row of every ROLE is read for `declared_by`, by the same rule
// engineIngestDestinationUnused follows: nothing says the row that reads a key is in the role
// whose prefix the key sits under, and a ledger that missed one would offer to delete bytes a
// model still loads.
func (a engineAdminAPI) engineLedgerFor(ctx context.Context, role string) (*engineLedger, *apiRefusal) {
	if a.mgr == nil || a.mgr.store == nil {
		return nil, &apiRefusal{apiError: internalErr(errors.New("no store"))}
	}
	ing := a.reg.ingester()
	if ing == nil || ing.storageChecker() == nil {
		return nil, refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment declares no model bucket, so there is no ledger to read", nil, nil)
	}
	prefix := strings.TrimSpace(role) + "/"
	objects, err := ing.storageChecker().list(ctx, prefix)
	if err != nil {
		// 🔴 Never an empty ledger. "The bucket is empty" and "the listing was refused" send the
		// reader to opposite acts, and the first of them is the one that deletes rows.
		return nil, refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"the bucket could not be listed under "+prefix+" ("+err.Error()+"), so this deployment"+
				" cannot say what it is storing — the answer is not that there is nothing", nil, nil)
	}
	models, err := a.mgr.store.ListEngineModels(ctx, "")
	if err != nil {
		return nil, &apiRefusal{apiError: internalErr(err)}
	}
	jobs, err := a.mgr.store.ListEngineIngestJobsForStorage(ctx, role)
	if err != nil {
		// Not fatal: the bucket and the catalogue are the ledger's two halves, and a job only
		// adds progress and provenance to a line that already exists.
		log.Printf("engines: the ledger could not read %s's ingest jobs (%v): objects will carry no job state", role, err)
		jobs = nil
	}
	ledger := engineLedgerJoin(role, objects, models, jobs)
	// And the deletions somebody pressed that the bucket has not caught up with yet. Applied
	// AFTER the join rather than inside it: the join is what a golden pins (family × ledger
	// state), and this is wall-clock state of one process.
	ledger.applyDeleting(ing.storageChecker().deleting())
	return ledger, nil
}

// applyDeleting draws the press. Until this existed, 消す answered 200 and the next listing looked
// exactly the same for as long as the task ran — measured on af-sandbox (2026-09-15, build
// 0a89569e), where the only visible outcome was the object turning into `バイト列がありません`
// minutes later, which reads as a failure rather than as a success.
//
// 🔴 It carries no job id, because there is no job row to dismiss: the deletion is an ECS task the
// CP started and does not own (ADR 0072 decision 7). The Console draws "deleting" and no button.
func (l *engineLedger) applyDeleting(started map[string]time.Time) {
	for key, at := range started {
		row := l.at(key)
		if row == nil || row.State != engineStoragePresent {
			// Already gone, or never this engine's: the press has landed, and a line for it would
			// be the very "it did not work" this answers.
			continue
		}
		// A deletion in flight outranks whatever an earlier job said about the key: it is the act
		// happening now, and it is the one that decides whether these bytes are still here.
		row.Job = &engineObjectJob{State: engineObjectDeleting, CreatedAt: at.UTC().Format(time.RFC3339)}
	}
}

// engineLedgerJoin is the pure half, so the classification can be pinned as a golden without a
// bucket, a store or a clock (engine_objects_test.go).
func engineLedgerJoin(role string, objects []engineStorageObject, models []store.EngineModel,
	jobs []store.EngineIngestJob) *engineLedger {
	prefix := strings.TrimSpace(role) + "/"
	l := &engineLedger{role: role, byKey: map[string]*engineObjectRow{},
		checkedAt: time.Now().UTC().Format(time.RFC3339)}
	// 🔴 The rows are built as pointers in a map and copied into the slice at the end. Appending
	// to the slice while handing pointers into it out reallocates the backing array, and every
	// pointer taken before the growth then addresses a row nobody reads again — which looked
	// exactly like a catalogue whose declarations had vanished.
	add := func(key string) *engineObjectRow {
		if row, ok := l.byKey[key]; ok {
			return row
		}
		dir := engineObjectRoleDir(role, key)
		row := &engineObjectRow{
			Key: key, RoleDir: dir, Placement: engineObjectPlacement(role, key, dir),
			State: engineStorageMissing, DeclaredBy: []engineObjectDeclaredBy{},
			tenants: map[string]struct{}{},
		}
		l.byKey[key] = row
		return row
	}
	for _, o := range objects {
		row := add(o.Key)
		row.State, row.Bytes = engineStoragePresent, o.Bytes
		if !o.LastModified.IsZero() {
			row.LastModified = o.LastModified.UTC().Format(time.RFC3339)
		}
	}
	// A row that points at nothing is a ledger fact too (decision 2), so a declared key inside
	// this prefix that the listing did not return is carried as `missing` rather than dropped.
	// A key OUTSIDE the prefix belongs to another engine's ledger and is left to it.
	for _, m := range models {
		for _, f := range m.Files {
			key := strings.TrimSpace(f.S3Key)
			if key == "" || !strings.HasPrefix(key, prefix) {
				continue
			}
			row := add(key)
			row.DeclaredBy = append(row.DeclaredBy,
				engineObjectDeclaredBy{ModelID: m.ID, Flag: strings.TrimSpace(f.Flag)})
			if row.Source == "" {
				row.Source = strings.TrimSpace(f.Source)
			}
			if row.ArtifactIdentity == "" {
				row.ArtifactIdentity = strings.TrimSpace(f.ArtifactIdentity)
			}
			if row.License == "" {
				row.License = engineFirstNonEmpty(m.LicenseName, m.License)
			}
			if row.Bytes == 0 {
				row.Bytes = f.Bytes
			}
			if t := strings.TrimSpace(m.LicenseAcceptedTenant); t != "" {
				row.tenants[t] = struct{}{}
			}
		}
	}
	// Newest first, which is the order the storage queries answer in: the first job seen for a
	// key is the one that describes it, and the ones behind it are history.
	seen := map[string]bool{}
	for _, j := range jobs {
		key := strings.TrimSpace(j.S3Key)
		if key == "" || !strings.HasPrefix(key, prefix) {
			continue
		}
		row := add(key)
		if t := strings.TrimSpace(j.TenantID); t != "" {
			row.tenants[t] = struct{}{}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		switch j.State {
		case store.EngineIngestPending, store.EngineIngestRunning:
			row.Job = &engineObjectJob{ID: j.ID, State: engineObjectUploading,
				Message: j.Message, CreatedAt: j.CreatedAt}
			if row.State != engineStoragePresent {
				row.State = engineObjectUploading
			}
		case store.EngineIngestFailed:
			row.Job = &engineObjectJob{ID: j.ID, State: engineObjectFailed,
				Message: j.Message, CreatedAt: j.CreatedAt}
			if row.State != engineStoragePresent {
				row.State = engineObjectFailed
			}
		default:
			// A finished job leaves provenance and nothing to press. The row's own declaration
			// wins where it has one: that is the deployment's current statement about the file,
			// while the job describes the upload that put it there.
			if row.Source == "" {
				row.Source = strings.TrimSpace(j.Source)
			}
			var spec engineIngestRequest
			if json.Unmarshal([]byte(j.Spec), &spec) == nil {
				if row.ArtifactIdentity == "" {
					row.ArtifactIdentity = strings.TrimSpace(spec.Resolved.ArtifactIdentity)
				}
				if row.License == "" {
					row.License = strings.TrimSpace(spec.AcceptedLicense)
				}
			}
			if row.Bytes == 0 {
				row.Bytes = j.Bytes
			}
		}
	}
	l.objects = make([]engineObjectRow, 0, len(l.byKey))
	for key, row := range l.byKey {
		// 🔴 No bytes and nobody pointing at them is not a ledger entry at all — it is the memory
		// of a job. Measured on af-sandbox (2026-09-15, build 0a89569e): after 消す ran MODE=delete
		// and the object really went, the key came back as `バイト列がありません` with no button on
		// it, because a `done` job still named it. To the operator that reads as "消す did not
		// work". Decision 6 settles it: a finished job is provenance ON an object, not a record OF
		// one, so once neither the bucket nor a row says the key exists, it stops being a line.
		//
		// `missing` stays for the case it was written for — a ROW pointing at nothing — because
		// that is a fault somebody has to fix, and it has a button (揃える, or forget the row).
		// A `failed` or `uploading` key is kept for the same reason: a task could still write it.
		if row.State == engineStorageMissing && len(row.DeclaredBy) == 0 {
			delete(l.byKey, key)
			continue
		}
		l.objects = append(l.objects, *row)
	}
	sort.Slice(l.objects, func(i, j int) bool { return l.objects[i].Key < l.objects[j].Key })
	// And the map re-pointed at the settled slice, so `at` and `inDir` answer the same rows the
	// wire does.
	for i := range l.objects {
		l.byKey[l.objects[i].Key] = &l.objects[i]
	}
	return l
}

// engineLedgerVisible narrows the ledger to what a granted tenant_admin may see: the objects its
// own jobs produced or its own rows declare (the rule ListEngineIngestJobsByTenant already
// applies to the job list). A super_admin sees the prefix.
//
// 🔴 The bucket is the OPERATOR's. A granted tenant sees what it put there and nothing else —
// not because another tenant's model name is a secret (every model id is visible from every
// tenant, engine_ingest_perm.go says so out loud) but because the ledger's acts are deletion and
// registration, and neither may reach bytes somebody else paid for.
func engineLedgerVisible(l *engineLedger, g engineIngestGrant) []engineObjectRow {
	if g.super {
		return l.objects
	}
	out := make([]engineObjectRow, 0, len(l.objects))
	for _, row := range l.objects {
		if _, ok := row.tenants[g.tenantID]; ok {
			out = append(out, row)
		}
	}
	return out
}

func engineLedgerMayTouch(row *engineObjectRow, g engineIngestGrant) bool {
	if g.super {
		return true
	}
	if row == nil {
		return false
	}
	_, ok := row.tenants[g.tenantID]
	return ok
}

// registerEngineObjectRoutes is the ledger's four routes, together (ADR 0085 decisions 2, 3, 7).
//
// One function called from one line of registerEngineAdminRoutes on purpose: three lanes were
// writing this ADR's P1 at once, and a route table every one of them appends to is one merge
// conflict per lane.
func registerEngineObjectRoutes(mux *http.ServeMux, a engineAdminAPI) {
	// The ledger itself. Under the ingest authority rather than super_admin: the scope is the
	// grant's, and a granted tenant that may take a model in has to be able to see what its own
	// downloads left in the bucket.
	mux.HandleFunc("GET /api/admin/engines/{key}/objects", a.withIngestAdmin(a.getObjects))
	// Registering a checkpoint that is already in the bucket — the road back for a deployment
	// that finds itself where af-sandbox was on 2026-09-15: bytes present, rows gone.
	mux.HandleFunc("POST /api/admin/engines/{key}/objects/register", a.withIngestAdmin(a.postObjectRegister))
	// And forgetting the bytes. The CP has no s3:DeleteObject (ADR 0072 decision 7), so this is
	// MODE=delete on the ingest task, refused while any row declares the key.
	mux.HandleFunc("DELETE /api/admin/engines/{key}/objects", a.withIngestAdmin(a.deleteObject))
	// The row as the subject: what the family reads, what the row has, what the bucket holds.
	mux.HandleFunc("POST /api/admin/engines/{key}/models/{id}/complete", a.withIngestAdmin(a.completeModel))
}

// engineObjectsEngine is the four routes' shared preamble: the engine exists, and its catalogue
// is this deployment's to write.
func (a engineAdminAPI) engineObjectsEngine(w http.ResponseWriter, r *http.Request, what string) (*engineRuntimeState, bool) {
	key := strings.TrimSpace(r.PathValue("key"))
	e := a.reg.get(key)
	if e == nil {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineUnknown, "no engine " + key})
		return nil, false
	}
	if what != "" && a.refuseBorrowedWrite(w, e, what) {
		return nil, false
	}
	return e, true
}

// getObjects (GET …/objects) is the ledger.
func (a engineAdminAPI) getObjects(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	e, ok := a.engineObjectsEngine(w, r, "")
	if !ok {
		return
	}
	// A borrowed engine's bucket is the far deployment's, and this deployment cannot list it at
	// all — the refusal names where to go rather than answering an empty prefix.
	if e.def.remote() {
		writeAPIRefusal(w, refuse(http.StatusBadRequest, errCodeEngineNotOurs,
			"engine "+e.def.Key+" is borrowed from "+e.def.URL+", so its bucket is that deployment's to read",
			nil, nil))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), engineStorageListTimeout)
	defer cancel()
	ledger, aerr := a.engineLedgerFor(ctx, e.def.Key)
	if aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, engineObjectsResponse{
		Objects: engineLedgerVisible(ledger, g), CheckedAt: ledger.checkedAt,
	})
}

// engineObjectRegisterBody is one press of 登録 on a main file nobody declares.
type engineObjectRegisterBody struct {
	Key string `json:"key"`
	// BaseModel is the family, when the operator knows it. The CP proposes one from the file's
	// own name and leaves it EMPTY when nothing matches confidently — a wrong family silences
	// `base_model_missing`, which is the row's only mark that it cannot generate.
	BaseModel string `json:"base_model"`
	// ID overrides the proposal. Offered because the proposal is a file name and the catalogue's
	// id is what `generate_image` will be asked for.
	ID string `json:"id"`
}

type engineObjectRegisterAnswer struct {
	ModelID string `json:"model_id"`
	// Moved says the bytes are being relocated to the key a loader lists. False is the ordinary
	// answer for an object that was already in the right place.
	Moved    bool                 `json:"moved"`
	Jobs     []map[string]any     `json:"jobs"`
	Complete engineCompleteAnswer `json:"complete"`
}

// postObjectRegister (POST …/objects/register) turns bytes nobody declares into the checkpoint
// row they belong to, and then completes that row.
//
// 🔴 This is the road the af-sandbox operator could not find on 2026-09-15, and the shape of what
// they had to do by hand is why it is one press: forget three failed jobs, register a row with
// the WRONG key on purpose under the SAME id an old job used (because a done job's model_id
// counts as a holder and the move refuses a different id as "also declared by"), then press
// 揃える. Nobody can be expected to find that.
//
// The licence acceptance is deliberately NOT recorded: assigning bytes that are already here is
// not the human act of accepting a licence, and the person registering them may not be the person
// who accepted anything. The row is created disabled, like every other row this panel writes.
func (a engineAdminAPI) postObjectRegister(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	e, ok := a.engineObjectsEngine(w, r, "registering an object")
	if !ok {
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	var b engineObjectRegisterBody
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b)
	}
	role := e.def.Key
	key := strings.TrimSpace(b.Key)
	if aerr := engineObjectKeyInRole(role, key); aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	ctx := r.Context()
	ledger, aerr := a.engineLedgerFor(ctx, role)
	if aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	object := ledger.at(key)
	if !engineLedgerMayTouch(object, g) {
		object = nil
	}
	// The bucket is asked directly rather than trusted from the listing: this is the call that
	// writes a row, and a listing seconds old is a display hint (the same rule verified reuse
	// follows).
	if aerr := a.engineObjectMustExist(ctx, key); aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	if aerr := engineObjectRegisterable(object, key); aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	rows := e.catalog.list(ctx)
	id, aerr := engineObjectProposedID(strings.TrimSpace(b.ID), key, rows)
	if aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	family := strings.TrimSpace(b.BaseModel)
	if family == "" {
		family = engineFamilyGuess(e.def.Provider, path.Base(key))
	}
	if family != "" && !engineBaseModelValid(e.def.Provider, family) {
		writeAPIRefusal(w, refuse(http.StatusBadRequest, errCodeEngineBadBody,
			"base_model must be one of "+strings.Join(engineBaseModelsFor(e.def.Provider), ", ")+
				" (this engine runs "+e.def.Provider+", which picks a workflow by family and will not guess one)",
			nil, nil))
		return
	}
	m := store.EngineModel{
		Role: role, ID: id, Kind: engineObjectRowKind(e), BaseModel: family,
		// The file stays where it IS. The move is `complete`'s act below, and it is the ingest
		// task's job to perform: MoveEngineModelFile rewrites a declaration the row already
		// holds, so a row registered at the destination would have nothing to move.
		Files: []store.EngineModelFile{{
			S3Key: key, Bytes: object.Bytes, Source: object.Source,
			ArtifactIdentity: object.ArtifactIdentity,
		}},
	}
	created, err := a.mgr.store.CreateEngineModel(ctx, m)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !created {
		writeAPIRefusal(w, refuse(http.StatusConflict, errCodeIngestIDExists,
			"the catalogue already holds a row called "+id+" for engine "+role,
			&apiHolder{Kind: "row", ID: id}, &apiNext{Act: "complete", Target: id}))
		return
	}
	e.catalog.invalidate()
	a.auditFor(r, g, "engine."+role+".model", "register "+id+" from the bucket ("+key+")")
	log.Printf("engines: %s catalogue row registered from the ledger: %s (%s, disabled)", role, id, key)
	// And the second half of the press: the misplaced weights are moved and everything the ledger
	// already holds for this family is declared. Downloads are NOT started here — they would
	// spend a licence acceptance this route deliberately did not take.
	answer, jobs, aerr := a.engineCompleteRun(ctx, r, g, e, id, engineCompleteBody{}, ledger, false)
	if aerr != nil {
		// The row stands. It is the thing that makes the object visible and repairable at all, and
		// the refusal says what to press next on it.
		writeAPIRefusal(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, engineObjectRegisterAnswer{
		ModelID: id, Moved: answer.Action == engineCompleteMoving, Jobs: jobs, Complete: answer,
	})
}

// engineObjectRowKind is what a row registered from bytes is: a checkpoint for an image engine, a
// gguf for a chat one. The same rule the ingest form applies, and the one thing a key cannot say.
func engineObjectRowKind(e *engineRuntimeState) string {
	if e.def.api() == engineAPIChat {
		return "gguf"
	}
	return "checkpoint"
}

// engineObjectKeyInRole keeps the route from becoming an existence oracle for another prefix —
// the property `GET …/storage` has by having no key parameter at all, kept here by checking one.
func engineObjectKeyInRole(role, key string) *apiRefusal {
	switch {
	case key == "":
		return refuse(http.StatusBadRequest, errCodeEngineBadBody, "key is required", nil, nil)
	case !strings.HasPrefix(key, strings.TrimSpace(role)+"/"):
		return refuse(http.StatusBadRequest, errCodeEngineBadBody,
			"key must be an object of this engine's own prefix ("+strings.TrimSpace(role)+"/)", nil, nil)
	}
	return nil
}

// engineObjectMustExist is the HeadObject the register and the delete both owe: a 404 for a key
// with nothing at it, and a 503 — never a 404 — when the deployment could not ask. Treating an
// inability to look as absence is what would register a row pointing at nothing.
func (a engineAdminAPI) engineObjectMustExist(ctx context.Context, key string) *apiRefusal {
	ing := a.reg.ingester()
	if ing == nil || ing.storageChecker() == nil {
		return refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment cannot check the bucket right now", nil, nil)
	}
	switch check := ing.storageChecker().verify(ctx, key); check.State {
	case engineStoragePresent:
		return nil
	case engineStorageMissing:
		return refuse(http.StatusNotFound, errCodeEngineBadBody,
			"there is nothing at "+key+" — this key is not in the bucket",
			&apiHolder{Kind: "object", Key: key}, nil)
	default:
		return refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"the bucket could not be asked about "+key+"; access denied and missing configuration"+
				" are not treated as absence",
			&apiHolder{Kind: "object", Key: key}, &apiNext{Act: "wait"})
	}
}

// engineObjectRegisterable is every reason these bytes are not a model to register.
//
// 🔴 A part gets no button of its own (decision 3). An encoder or a VAE registered as its own row
// is what put parts in the registered list beside the checkpoints, where they can never be
// enabled and help nothing — they are attached by the 揃える of whichever checkpoint reads them.
func engineObjectRegisterable(object *engineObjectRow, key string) *apiRefusal {
	if object == nil {
		return refuse(http.StatusNotFound, errCodeEngineBadBody,
			"the ledger holds no object at "+key+" for this engine",
			&apiHolder{Kind: "object", Key: key}, nil)
	}
	if len(object.DeclaredBy) > 0 {
		id := object.DeclaredBy[0].ModelID
		return refuse(http.StatusConflict, errCodeEngineBadBody,
			key+" is already declared by "+id+" — that row's 揃える is what repairs it",
			&apiHolder{Kind: "row", ID: id, Key: key}, &apiNext{Act: "complete", Target: id})
	}
	if object.Job != nil && object.Job.State == engineObjectUploading {
		return refuse(http.StatusConflict, errCodeEngineBadBody,
			"an ingest is still writing "+key+" — it will register its own row when it lands",
			&apiHolder{Kind: "job", ID: object.Job.ID, Key: key}, &apiNext{Act: "wait"})
	}
	// The path has to NAME a main-file directory. Misplaced is the normal case here (that is what
	// this button is for), so it is the directory the bytes were aimed at that decides, not the
	// one they should end up in.
	if !engineObjectIsMainFile(key) {
		return refuse(http.StatusBadRequest, errCodeEngineBadBody,
			key+" is not a checkpoint: a part is attached by the 揃える of the model that reads it,"+
				" never registered as a model of its own", &apiHolder{Kind: "object", Key: key}, nil)
	}
	return nil
}

// engineObjectIsMainFile answers whether a key names one of the two directories a model's OWN
// weights live in, at any depth — `image/checkpoints/split_files/diffusion_models/x.safetensors`
// is af-sandbox's measured shape and has to qualify.
func engineObjectIsMainFile(key string) bool {
	if !engineModelFileName(key) {
		return false
	}
	return strings.Contains(key, "/checkpoints/") || strings.Contains(key, "/diffusion_models/")
}

// engineObjectProposedID is the id a registered row gets: the file's stem without the
// quantisation tag that names the file rather than the model, made unique against the catalogue
// by suffix.
//
// 🔴 An id the operator SUPPLIED is never silently suffixed. "Register this as anima" answering
// with a row called `anima-2` is the kind of quiet difference that ends in the wrong model being
// enabled; the collision is a refusal that names the row in the way.
func engineObjectProposedID(want, key string, rows []store.EngineModel) (string, *apiRefusal) {
	taken := map[string]bool{}
	for _, m := range rows {
		taken[strings.TrimSpace(m.ID)] = true
	}
	if want != "" {
		if taken[want] {
			return "", refuse(http.StatusConflict, errCodeIngestIDExists,
				"the catalogue already holds a row called "+want,
				&apiHolder{Kind: "row", ID: want}, &apiNext{Act: "complete", Target: want})
		}
		return want, nil
	}
	base := engineObjectIDFromKey(key)
	if base == "" {
		return "", refuse(http.StatusBadRequest, errCodeEngineBadBody,
			"no id could be proposed from "+key+" — say which id this model should have", nil, nil)
	}
	id := base
	for n := 2; taken[id]; n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	return id, nil
}

// engineObjectIDFromKey is the Console's engineIdFromFile, in Go and reading a bucket key. The two
// strip the same two things — the extension and the quantisation/precision tag — because two
// quantisations of one model are one model, and the suffix is what names the FILE.
//
// ⚠️ Written here rather than shared with the plan's proposal (ADR 0085 decision 4): that one is
// resolving an UPSTREAM file and is another lane's. If the two ever disagree the plan's wins —
// it has the repository's own metadata and this has a path.
func engineObjectIDFromKey(key string) string {
	name := strings.ToLower(path.Base(strings.TrimSpace(key)))
	for _, ext := range engineModelFileExts {
		if strings.HasSuffix(name, ext) {
			name = strings.TrimSuffix(name, ext)
			break
		}
	}
	for _, tag := range engineObjectPrecisionTags {
		for _, sep := range []string{"-", "."} {
			if strings.HasSuffix(name, sep+tag) {
				return strings.TrimSuffix(name, sep+tag)
			}
		}
	}
	return name
}

// engineObjectPrecisionTags are the endings that name a build rather than a model. Not a regular
// expression: the Console's is one because JavaScript has nothing cheaper, and a literal list is
// what a reader can check against a file name they are holding.
var engineObjectPrecisionTags = []string{
	"f16", "fp16", "bf16", "f32", "fp32", "int8", "fp8", "fp8_scaled", "fp8_e4m3fn", "fp8_e5m2",
	"q2_k", "q3_k_s", "q3_k_m", "q3_k_l", "q4_0", "q4_1", "q4_k_s", "q4_k_m",
	"q5_0", "q5_1", "q5_k_s", "q5_k_m", "q6_k", "q8_0",
}

// engineObjectDeleteBody is one press of 消す on an orphan.
type engineObjectDeleteBody struct {
	Key string `json:"key"`
}

type engineObjectDeleteAnswer struct {
	// Deleting rather than `deleted`: the CP has no s3:DeleteObject and never gets one (ADR 0072
	// decision 7), so what this route returns is a task that was started, not bytes that are gone.
	Deleting string `json:"deleting"`
}

// deleteObject (DELETE …/objects) forgets the bytes of an object nothing declares.
func (a engineAdminAPI) deleteObject(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	e, ok := a.engineObjectsEngine(w, r, "deleting an object")
	if !ok {
		return
	}
	var b engineObjectDeleteBody
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b)
	}
	role, key := e.def.Key, strings.TrimSpace(b.Key)
	if aerr := engineObjectKeyInRole(role, key); aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	ctx := r.Context()
	ledger, aerr := a.engineLedgerFor(ctx, role)
	if aerr != nil {
		writeAPIRefusal(w, aerr)
		return
	}
	object := ledger.at(key)
	if !engineLedgerMayTouch(object, g) || object == nil || object.State == engineStorageMissing {
		writeAPIRefusal(w, refuse(http.StatusNotFound, errCodeEngineBadBody,
			"the ledger holds no object at "+key+" for this engine",
			&apiHolder{Kind: "object", Key: key}, nil))
		return
	}
	if len(object.DeclaredBy) > 0 {
		id := object.DeclaredBy[0].ModelID
		writeAPIRefusal(w, refuse(http.StatusConflict, errCodeEngineBadBody,
			key+" is declared by "+id+", and deleting it would leave that row pointing at nothing"+
				" — forget the row first, or delete it with ?purge=1",
			&apiHolder{Kind: "row", ID: id, Key: key}, &apiNext{Act: "forget_row", Target: id}))
		return
	}
	if object.Job != nil && object.Job.State == engineObjectUploading {
		writeAPIRefusal(w, refuse(http.StatusConflict, errCodeEngineBadBody,
			"an ingest is still writing "+key+" — forgetting the row does not stop the task, and the"+
				" task would write these bytes again after the delete",
			&apiHolder{Kind: "job", ID: object.Job.ID, Key: key}, &apiNext{Act: "wait"}))
		return
	}
	// Pressing 消す twice is the SAME act, not an error: the first task is still running and the
	// object is still listed, which is exactly what makes somebody press again. Answering 200
	// without starting a second task keeps the button honest and the bill unchanged.
	if object.Job != nil && object.Job.State == engineObjectDeleting {
		writeJSON(w, http.StatusOK, engineObjectDeleteAnswer{Deleting: key})
		return
	}
	ing := a.reg.ingester()
	if ing == nil {
		writeAPIRefusal(w, refuse(http.StatusServiceUnavailable, errCodeIngestUnavailable,
			"this deployment declares no ingest task, and the Control Plane may not write to the bucket"+
				" itself — delete "+key+" by hand", nil, nil))
		return
	}
	if err := ing.deleteObjects(ctx, []string{key}); err != nil {
		writeAPIErr(w, &apiError{http.StatusBadGateway, errCodeEngineECSError, err.Error()})
		return
	}
	// The press is recorded before the answer, so the very next ledger read draws it.
	ing.storageChecker().markDeleting(key)
	a.auditFor(r, g, "engine."+role+".object", "delete "+key)
	writeJSON(w, http.StatusOK, engineObjectDeleteAnswer{Deleting: key})
}
