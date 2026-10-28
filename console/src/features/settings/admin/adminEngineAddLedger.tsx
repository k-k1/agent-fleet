/** バケツ — the bucket, listed (ADR 0085 decision 2 and 7).
 *
 * 🔴 Orphans and misplaced objects sort first because they are the only rows here anybody has to
 * act on: everything else is provenance for a row that already works. A part gets no button of
 * its own — it is attached, and moved, by the 揃える of whichever checkpoint reads it. */
function EngineLedger({ objects, role, image, failed, checkedAt, busy, readOnly, deletingKeys, onRegister, onDelete, onDismiss, onComplete }: {
  objects: EngineObjectRow[] | null;
  /** The engine's own key, which is the prefix every one of these objects sits under. Needed
   *  because what counts as a model's own weights is read off the key, and the two roles write
   *  different layouts under it. */
  role: string;
  image: boolean;
  failed: boolean;
  checkedAt: string;
  busy: string;
  readOnly: boolean;
  /** Keys this Console asked to delete and the bucket still lists. Drawn as `deleting` until the
   *  listing drops them; the CP's own `job.state === "deleting"` says the same thing once it
   *  starts sending it, and either is enough. */
  deletingKeys: string[];
  onRegister: (object: EngineObjectRow) => void;
  onDelete: (object: EngineObjectRow) => void;
  onDismiss: (id: string) => void;
  onComplete: (modelId: string) => void;
}) {
  const tr = useT();
  const sorted = [...(objects || [])]
    // 🔴 A `missing` entry nobody declares is not a fact about anything: the row it belonged to is
    // gone and so are the bytes. The CP stopped sending those; one that arrives anyway is dropped
    // rather than drawn as a line with no subject and no act.
    .filter((object) => object.state !== "missing" || (object.declared_by || []).length > 0)
    .sort((left, right) => ledgerRank(left) - ledgerRank(right) || left.key.localeCompare(right.key));
  return <section className="engine-ledger" aria-label={tr("admin.catalog_ledger_title" as never)}>
    <header className="engine-ledger-head">
      <strong>{tr("admin.catalog_ledger_title" as never)}</strong>
      {checkedAt && <span className="muted">{(tr("admin.catalog_ledger_checked" as never) as string).replace("{t}", fmtDateTime(checkedAt))}</span>}
    </header>
    {/* What the list IS, always — and what to press, only where there is something to press. A
        borrowed row's catalogue is the far deployment's mirror and draws no act at all (ADR 0079
        decision 7), so the second sentence there would describe a button nobody has. */}
    <p className="admin-hint">{tr("admin.catalog_ledger_note" as never)}
      {!readOnly && <> {tr("admin.catalog_ledger_note_acts" as never)}</>}</p>
    {objects === null && <p className={failed ? "form-err" : "muted"}>{tr((failed ? "admin.catalog_ledger_unavailable" : "admin.catalog_storage_checking") as never)}</p>}
    {objects !== null && !objects.length && <p className="muted">{tr("admin.catalog_ledger_empty" as never)}</p>}
    {!!sorted.length && <ul className="engine-ledger-list">{sorted.map((object) => {
      const orphan = !(object.declared_by || []).length;
      const removing = deletingKeys.includes(object.key) || object.job?.state === "deleting";
      const taking = !removing && ingesting(object);
      const pending = busy === `object:${object.key}` || (!!object.job && busy === `job:${object.job.id}`);
      const holder = (object.declared_by || [])[0]?.model_id || "";
      return <li key={object.key} className={`engine-ledger-row${orphan ? " orphan" : ""}${removing ? " deleting" : ""}${taking ? " uploading" : ""}`} aria-label={object.key}>
        <span className="mono engine-ledger-key">{object.key}</span>
        <span className="engine-ledger-tags">
          <span className="engines-model-tag">{object.role_dir}</span>
          {object.placement === "misplaced" && <span className="engines-model-tag warn">{tr("admin.catalog_ledger_misplaced" as never)}</span>}
          <span className={`engines-model-tag ${removing || taking ? "lead" : object.state === "present" ? "on" : object.state === "failed" || object.state === "missing" ? "bad" : "lead"}`}>
            {tr((removing ? "admin.catalog_ledger_state_deleting"
              : taking ? "admin.catalog_ledger_state_uploading"
                : `admin.catalog_ledger_state_${object.state}`) as never)}
          </span>
          {!!object.bytes && <span className="engines-model-tag">{formatBytes(object.bytes)}</span>}
          {object.license && <span className="engines-model-tag">{object.license}</span>}
        </span>
        <span className="muted engine-ledger-declared">{orphan
          ? tr("admin.catalog_ledger_orphan" as never)
          : (tr("admin.catalog_ledger_declared" as never) as string).replace("{m}", (object.declared_by || [])
            .map((holder) => holder.flag ? `${holder.model_id} (${holder.flag})` : holder.model_id).join(", "))}</span>
        {object.source && <span className="muted mono engine-ledger-source">{object.source}</span>}
        {removing && <span className="muted engine-ledger-note">{tr("admin.catalog_ledger_deleting_note" as never)}</span>}
        {taking && <span className="muted engine-ledger-note">{tr("admin.catalog_ledger_uploading_note" as never)}</span>}
        {/* A row points at bytes that are not there. The ledger says whose problem it is and the
            act is that row's 揃える — the object side has nothing to press. */}
        {!removing && object.state === "missing" && !!holder && <span className="form-err engine-ledger-note">
          {(tr("admin.catalog_ledger_missing_note" as never) as string).replace("{m}", holder)}</span>}
        {object.job?.message && <span className="form-err engine-ledger-message">{object.job.message}</span>}
        {/* Nothing is pressable while a task owns the key. 登録 on a key an ingest is writing
            answers 409 `already declared by` — the refusal is right and the button was not. */}
        {!readOnly && !removing && !taking && <span className="engine-ledger-acts">
          {object.state === "missing" && !!holder
            ? <Button small variant="primary" disabled={pending} aria-label={`${tr("admin.catalog_complete" as never)}: ${object.key}`}
              onClick={() => onComplete(holder)}>{tr("admin.catalog_complete" as never)}</Button>
            : object.state === "failed" && object.job
            ? <Button small variant="danger" disabled={pending} aria-label={`${tr("admin.catalog_ledger_delete" as never)}: ${object.key}`}
              onClick={() => onDismiss(object.job!.id)}>{tr("admin.catalog_ledger_delete" as never)}</Button>
            : <>
              {orphan && object.state === "present" && isMainObject(object, role, image) && <Button small variant="primary" disabled={pending}
                aria-label={`${tr("admin.catalog_ledger_register" as never)}: ${object.key}`}
                onClick={() => onRegister(object)}>{tr("admin.catalog_ledger_register" as never)}</Button>}
              {orphan && object.state === "present" && <Button small variant="danger" disabled={pending}
                aria-label={`${tr("admin.catalog_ledger_delete" as never)}: ${object.key}`}
                onClick={() => onDelete(object)}>{tr("admin.catalog_ledger_delete" as never)}</Button>}
            </>}
        </span>}
      </li>;
    })}</ul>}
  </section>;
}

/** Whether a task is writing this key right now. Both spellings count: the ledger promotes an
 * object to `uploading` only when the bucket does not hold it yet, so a re-ingest over an
 * existing key is `present` with an `uploading` job — which is exactly the row that offered 登録
 * and answered 409 `already declared by` on af-sandbox. */
function ingesting(object: EngineObjectRow): boolean {
  return object.state === "uploading" || object.job?.state === "uploading";
}

function inFlight(object: EngineObjectRow): boolean {
  return ingesting(object) || object.job?.state === "deleting";
}

/** What has to be looked at first: a failure, then anything nobody declares or that no loader can
 * see, then work in flight, then a row pointing at nothing. */
function ledgerRank(object: EngineObjectRow): number {
  if (object.state === "failed") return 0;
  if (object.placement === "misplaced" || !(object.declared_by || []).length) return 1;
  if (object.state === "uploading") return 2;
  if (object.state === "missing") return 3;
  return 4;
}

/** A main file — the thing a person means by "a model" — rather than a part. `register` is
 * offered on these only, and this is the CP's `engineObjectIsMainFile` in TypeScript: a button the
 * route would refuse is worse than no button.
 *
 * The image role is ComfyUI's, one directory per loader, so a misplaced file still names its role
 * directory in the key — which is how `image/checkpoints/split_files/diffusion_models/x.safetensors`
 * is recognised. The llm role is flat: `llm/<file>.gguf` and nothing deeper, because `llm/loras/…`
 * is an adapter and `llm/<name>/shard.gguf` is one piece of a file. */
function isMainObject(object: EngineObjectRow, role: string, image: boolean): boolean {
  if (image) {
    if (object.role_dir === "checkpoints" || object.role_dir === "diffusion_models") return true;
    return /(^|\/)(checkpoints|diffusion_models)\//.test(object.key);
  }
  const prefix = `${role.trim()}/`;
  if (!object.key.startsWith(prefix)) return false;
  const rest = object.key.slice(prefix.length);
  return !!rest && !rest.includes("/") && MODEL_FILE_EXTS.some((ext) => rest.toLowerCase().endsWith(ext));
}

/** The names a loader could load — the CP's `engineModelFileExts`. The image branch above has no
 * need of them (a key under `checkpoints/` is a model file by where it is), but a flat role has
 * only the name to go on, and a `.json` beside a GGUF is not a model. */
const MODEL_FILE_EXTS = [".safetensors", ".gguf", ".ckpt", ".pt", ".sft", ".bin"];
