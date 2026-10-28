/** The plan card (ADR 0085 decision 4). One press, and the CP decided what that press does.
 *
 * 🔴 No role selector, no key field, no parts checkbox, no attach/replace: every one of those was
 * a question a person who does not know what a text encoder is cannot answer, and getting one
 * wrong produced a row that looked complete and would not generate. What is left is the version
 * and the file — which repository and which of its files — and the licence. */
function IngestPlanDialog({ row, kind, hit, initialSource, initialRef, onClose, onStarted }: {
  row: EngineRow; kind: ModelKind; hit?: IngestHit;
  initialSource?: CatalogSource;
  /** Open the dialog as though this had been PASTED into the source box (ADR 0089) — a Hugging
   *  Face blob URL, which the parser below already turns into a repository, a revision and a
   *  file.
   *
   *  🔴 Its own prop and not a borrowed field of `hit`: `hit.ref` is the HF REVISION and
   *  `hit.model_ref` is the repository, so a URL smuggled through either arrives as one of those
   *  (measured — the request went out with `revision` set to the whole URL). This is a second way
   *  to fill the FORM and deliberately not a second way to start an ingest: the plan, the price
   *  and the licence are the same screen either way. */
  initialRef?: string;
  onClose: () => void; onStarted: (job: IngestJob) => void;
}) {
  const tr = useT();
  const image = engineIsImage(row);
  const isLora = kind === "lora";
  const [sourceType, setSourceType] = useState<CatalogSource>(hit?.source === "civitai" || initialSource === "civitai" || initialSource === "civitai-red" ? "civitai" : "hf");
  const [manualRef, setManualRef] = useState(initialRef || hit?.model_ref || hit?.ref || "");
  const [versions, setVersions] = useState<IngestVersion[]>([]);
  const [versionRef, setVersionRef] = useState(hit?.ref || "");
  const [files, setFiles] = useState<IngestCandidate[]>([]);
  const [file, setFile] = useState("");
  const [resolved, setResolved] = useState<ResolvedSource | null>(null);
  const [plan, setPlan] = useState<IngestPlan | null>(null);
  const [replanned, setReplanned] = useState(false);
  const [id, setId] = useState("");
  const [idEdited, setIdEdited] = useState(false);
  const [description, setDescription] = useState("");
  const [baseModel, setBaseModel] = useState("");
  // The family the OPERATOR picked, as opposed to the one the CP named. Separate state rather
  // than a comparison against the last answer: comparing makes the trigger flip back the moment
  // the CP echoes the pick, which re-plans a third time instead of stopping.
  const [operatorFamily, setOperatorFamily] = useState("");
  const [familyChoices, setFamilyChoices] = useState<string[]>([]);
  const [context, setContext] = useState("");
  const [output, setOutput] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [trainedWords, setTrainedWords] = useState((hit?.trained_words || []).join(", "));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<EngineApiError | null>(null);
  const inspectSeq = useRef(0);
  const filesSeq = useRef(0);
  const [params, setParams] = useState<Record<keyof EngineParams, string>>({
    steps: "", cfg: "", sampler: "", scheduler: "", clip_skip: "", guidance: "", shift: "", weight: "",
  });
  const rawRef = manualRef.trim();
  const hfURL = rawRef.match(/^https?:\/\/huggingface\.co\/([^/?#]+\/[^/?#]+)(?:\/(?:blob|resolve)\/([^/?#]+)\/([^?#]+))?/);
  const civitaiPage = rawRef.match(/^https?:\/\/(?:[\w-]+\.)*civitai\.(?:com|red)\/models\/(\d+)/);
  const civitaiVersionParam = rawRef.match(/[?&]modelVersionId=(\d+)/);
  const civitaiLegacy = rawRef.match(/^civitai:(\d+)$/);
  const civitaiModelRef = hit?.model_ref || civitaiPage?.[1] || "";
  const pastedVersion = (sourceType === "civitai" ? civitaiVersionParam?.[1] || civitaiLegacy?.[1] : hfURL?.[2]) || "";
  const repo = sourceType === "civitai" ? civitaiModelRef || pastedVersion
    : hfURL?.[1] || hit?.model_ref || rawRef || hit?.ref || "";
  const pastedFile = sourceType === "hf" ? hfURL?.[3] || "" : "";
  const civitaiVersionURL = /^https?:\/\/(?:[\w-]+\.)*civitai\.(?:com|red)\//.test(rawRef) && !!civitaiVersionParam;
  const plainURL = /^https?:\/\//.test(rawRef) && !hfURL && !civitaiVersionURL;
  const resetInspection = () => {
    ++inspectSeq.current; ++filesSeq.current;
    setVersions([]); setVersionRef(""); setFiles([]); setFile(""); setResolved(null); setPlan(null);
    setAccepted(false); setBusy(false); setErr(null);
  };
  const changeManualRef = (value: string) => {
    setManualRef(value);
    if (/^civitai:\d+$/.test(value.trim()) || (/civitai\.(?:com|red)\//.test(value) && /[?&]modelVersionId=\d+/.test(value))) setSourceType("civitai");
    else if (/huggingface\.co\//.test(value)) setSourceType("hf");
    resetInspection();
  };

  const sourceBody = useCallback((fileName = file) => {
    if (plainURL) return { url: repo, sha256: fileName.trim() };
    if (sourceType === "civitai") return { civitai: { versionId: Number(versionRef || hit?.ref), file: fileName } };
    return { hf: { repo, revision: versionRef, file: fileName } };
  }, [file, hit?.ref, plainURL, repo, sourceType, versionRef]);

  const loadFiles = useCallback(async (selectedVersion: string, parentSeq?: number) => {
    const seq = ++filesSeq.current;
    setErr(null); setFiles([]); setFile(""); setResolved(null); setPlan(null);
    const source = sourceType === "civitai"
      ? { civitai: { versionId: Number(selectedVersion), file: "" } }
      : { hf: { repo, revision: selectedVersion, file: "" } };
    const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/files`, "POST", { source });
    if (seq !== filesSeq.current || (parentSeq !== undefined && parentSeq !== inspectSeq.current)) return;
    if (answer?.error) { setErr(answer.error as EngineApiError); return; }
    const offered = (Array.isArray(answer?.files) ? answer.files : []) as IngestCandidate[];
    setFiles(offered);
    if (offered.length === 1) setFile(offered[0].name);
  }, [repo, row.key, sourceType]);

  const inspect = useCallback(async () => {
    if (!repo) return;
    const seq = ++inspectSeq.current;
    ++filesSeq.current;
    setBusy(true); setErr(null);
    try {
      if (plainURL) { setVersions([]); setFiles([]); return; }
      const requestedVersion = versionRef || pastedVersion;
      // A Civitai version with no model beside it used to short-circuit into a list of ONE, made
      // up here, because the route demanded a model id the Console did not have. The CP resolves
      // it from the version now, so the same question has one answer and one road to it — the
      // version list this then draws is the real one, and the other versions are selectable.
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/versions`, "POST", {
        source: sourceType, ref: hit?.ref || pastedVersion || repo, model_ref: sourceType === "civitai" ? civitaiModelRef : repo,
      });
      if (seq !== inspectSeq.current) return;
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      const offered = (Array.isArray(answer?.versions) ? answer.versions : []) as IngestVersion[];
      setVersions(offered);
      const first = offered.find((version) => version.ref === requestedVersion)?.ref || offered[0]?.ref || requestedVersion;
      setVersionRef(first);
      if (first) {
        await loadFiles(first, seq);
        if (seq === inspectSeq.current && pastedFile) setFile(pastedFile);
      }
    } finally { if (seq === inspectSeq.current) setBusy(false); }
  }, [civitaiModelRef, hit?.ref, loadFiles, pastedFile, pastedVersion, plainURL, repo, row.key, sourceType, versionRef]);

  // Opened ON something — a hit from the 探す tab, or a repository file from a catalogue card
  // (ADR 0089) — inspects itself. Opened empty, it waits for somebody to paste and press 調べる.
  useEffect(() => { if (hit || initialRef) void inspect(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, []);

  // The family the CP is asked to plan FOR: an image checkpoint's only, because that is what the
  // parts table and the main file's role are keyed by. An LLM LoRA's "base" is a registered
  // model's id, which is not a family and would be planned as an unknown one.
  const plannedFamily = image && !isLora ? baseModel : "";
  // What re-planning is triggered BY, which is not the same as what is sent. `baseModel` also
  // holds the family the CP named itself, and asking again with that changes nothing: the plan
  // that carried it was already built from it (enginePlanFor falls back to its own guess when the
  // body names none), so the second answer is identical. Measured on this screen: a row whose
  // family the CP can name resolved TWICE on open, blanking the plan card in between, and a
  // family it cannot name resolved once. Only an operator's pick belongs here.
  const chosenFamily = image && !isLora ? operatorFamily : "";
  // 🔴 The family is SENT, and a change to it re-plans (ADR 0094 decision 7). The parts a split
  // family needs are planned from it (engine_family_parts.go), so a card drawn while it is still
  // unknown prices the main file alone — and the press, which does send it, re-plans into three
  // and answers 409 `engine_plan_stale`. The person then accepts the licence again and presses
  // again, which is the "one press" this deployment's ingest is built around, spent twice.
  //
  // Reachable because a family the CP cannot guess is now a real case: the qwen-image-edit
  // families deliberately have no guess rule (a wrong family here silences the row's only mark —
  // engine_family_guess.go), so the selector is the ONLY place their family ever comes from.
  useEffect(() => {
    if (!file) return;
    let live = true;
    setResolved(null); setPlan(null); setAccepted(false); setReplanned(false);
    apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest/resolve`, "POST", {
      source: sourceBody(file), kind: isLora ? "lora" : image ? "checkpoint" : "gguf",
      ...(plannedFamily ? { base_model: plannedFamily } : {}),
    }).then((answer) => {
      if (!live) return;
      if (answer?.error) { setErr(answer.error as EngineApiError); return; }
      const found = answer as ResolvedSource;
      setResolved(found);
      if (found.plan) {
        setPlan(found.plan);
        if (!idEdited) setId(found.plan.id || "");
        if (found.plan.base_model) setBaseModel(found.plan.base_model);
        // Remembered, because the CP offers candidates only while it cannot name the family
        // itself: the re-plan our own choice causes answers with none, and the selector would
        // disappear under the person who just used it.
        if (found.plan.base_model_candidates?.length) setFamilyChoices(found.plan.base_model_candidates);
      }
      if (found.context_length && !context) {
        // 🔴 The ceiling is not the setting (ADR 0089). The CP sends `context_length` labelled as
        // the architecture's maximum, and pre-filling the field with it priced the KV cache at
        // that maximum: 66,560 MiB for a 27B (measured 2026-09-18), which reads as "this will
        // never run" about a model that runs fine at 32768. So the field opens at the largest
        // window that actually fits the box this engine buys, and the ceiling is shown beside it
        // as what it is.
        //
        // 🔴 And when it CANNOT be fitted — no readable header, no card — the fallback is not
        // the ceiling either. Falling back to it put 262,144 in the field of the af-sandbox row
        // that then asked for 16 GiB of KV cache and took the L4 out of memory, which is the
        // same failure ADR 0089 was written about, reached through the error path instead of the
        // happy one. windowWhenUnsized is a stated fallback and the form says so.
        const weights = found.bytes ? Math.round(found.bytes / 1048576) : 0;
        const fitted = windowThatFits(weights, kvPriceOf(found),
          row.class?.vram_mib || 0, found.context_length);
        const window = fitted || windowWhenUnsized(found.context_length);
        setContext(String(window)); setOutput(String(Math.floor(window / 8)));
      }
      if (found.params_hint) setParams((current) => ({
        ...current,
        ...Object.fromEntries(Object.entries(found.params_hint || {}).map(([key, value]) => [key, String(value)])),
      }));
      if (found.trained_words?.length && !trainedWords.trim()) setTrainedWords(found.trained_words.join(", "));
    });
    return () => { live = false; };
    // Existing typed settings deliberately outrank metadata suggestions.
    //
    // ⚠️ The effect SETS `baseModel` and must therefore not depend on it: `chosenFamily` is the
    // operator's pick alone, so the CP naming a family here does not re-enter. What is SENT stays
    // `plannedFamily` (the whole of `baseModel`), because the press sends it too and a plan built
    // without it would go stale against one.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file, image, isLora, row.key, sourceBody, chosenFamily]);

  // The family is asked ONLY when the CP could not read one, and only from the candidates it
  // knows. An LLM LoRA is pinned to a registered model rather than to a family name.
  const familyOptions = isLora && !image
    ? (row.model_rows || []).filter((model) => model.kind !== "lora").map((model) => model.id)
    : plan?.base_model_candidates?.length ? plan.base_model_candidates : familyChoices;
  const needsFamily = !plan?.base_model && familyOptions.length > 0;
  // Drawn while the answer is still ours to change, which outlasts `needsFamily`: once the choice
  // has been sent the plan names a family and the press is no longer blocked, but the person is
  // still looking at a card they may want to re-aim.
  const asksFamily = familyOptions.length > 0 && (needsFamily || !!baseModel);
  const bytes = plan?.bytes_to_download ?? resolved?.bytes ?? 0;
  const contextTokens = Number(context.trim().replace(/[_,]/g, "")) || 0;
  const weightsMiB = bytes ? Math.round(bytes / 1048576) : 0;
  const cardMiB = row.class?.vram_mib || 0;
  // The verdict, from the one module that owns it (ADR 0089). A LoRA is deliberately left out:
  // an adapter is loaded beside a checkpoint and its own size is not what decides the start.
  const fit = modelFit(weightsMiB, !image && !isLora ? kvPriceOf(resolved) : NO_KV,
    contextTokens, isLora ? 0 : cardMiB, row.classes || []);
  const kvMiB = fit.kvMiB;
  const needMiB = fit.needMiB;
  // The family this press would take in is the plan's when the CP read one, and the operator's
  // answer when it had to ask (ADR 0094 decision 8 — the measurement is shown for the family, so
  // it must follow the selector rather than the plan alone), narrowed to the build the plan would
  // actually stage.
  const ingestMeasured = image && !isLora
    ? familyVramMeasurement(plan?.base_model || baseModel, (plan?.files || []).map((f) => f.name))
    : null;
  const missing = (() => {
    if (!repo) return tr("admin.catalog_need_source" as never) as string;
    if (!plainURL && !versionRef) return tr("admin.catalog_need_version" as never) as string;
    if (!file) return tr(plainURL ? "admin.catalog_need_checksum" as never : "admin.catalog_need_file" as never) as string;
    if (!plan) return tr("admin.catalog_plan_building" as never) as string;
    if (resolved?.can_ingest === false) return tr("admin.engines_wizard_cannot") as string;
    if (!id.trim()) return tr("admin.engines_wizard_need_id") as string;
    if (needsFamily && !baseModel) return tr("admin.engines_wizard_need_family") as string;
    if (!accepted) return tr("admin.catalog_need_license" as never) as string;
    return "";
  })();

  const start = async () => {
    if (missing || !plan) return;
    setBusy(true); setErr(null);
    const n = (value: string) => { const parsed = Number(value.trim().replace(/[_,]/g, "")); return Number.isFinite(parsed) && parsed > 0 ? parsed : 0; };
    const paramsBody = Object.fromEntries(Object.entries(params).flatMap(([key, value]) => value.trim()
      ? [[key, key === "sampler" || key === "scheduler" ? value.trim() : n(value)]] : []));
    try {
      const answer = await apiJSON(`api/admin/engines/${encodeURIComponent(row.key)}/ingest`, "POST", {
        // 🔴 The source and the CP's own plan, and nothing else about the destination. The key,
        // the role, the parts and the reuse decision are all inside `plan_token`: three parties
        // deciding one key is what produced the 400 `s3Key must be empty or identical`.
        //
        // `kind` is the one thing the token cannot carry for us: the CP re-plans from this body
        // at the press and reads `lora` out of it to decide the directory (`image/loras/` versus
        // `image/checkpoints/`). Sent identical to the resolve's, or the re-plan disagrees with
        // the token — a 409 at best, an adapter staged under `checkpoints/` at worst.
        source: sourceBody(file), kind: isLora ? "lora" : image ? "checkpoint" : "gguf",
        plan_token: plan.plan_token,
        id: id.trim(), ...(baseModel ? { base_model: baseModel } : {}),
        description: description.trim(),
        ...(!image && !isLora ? { context_tokens: n(context), max_output_tokens: n(output) } : {}),
        ...(image ? { params: paramsBody } : {}),
        ...(isLora ? { trained_words: trainedWords.split(/[\n,]/).map((word) => word.trim()).filter(Boolean) } : {}),
        license_accepted: true,
      });
      if (answer?.error) {
        const error = answer.error as EngineApiError;
        // The plan is a quote and the press is the purchase. When anything material moved, the
        // CP answers the NEW plan and the card is redrawn from it — including the licence tick,
        // because what was accepted is not what would now be taken in.
        if (error.code === "engine_plan_stale" && error.plan) {
          setPlan(error.plan); setReplanned(true); setAccepted(false);
          if (!idEdited) setId(error.plan.id || "");
          return;
        }
        setErr(error);
        return;
      }
      onStarted(answer as IngestJob);
    } finally { setBusy(false); }
  };

  const planTitle = hit?.name
    ? `${tr("admin.catalog_plan_title" as never)} — ${hit.name}`
    : tr("admin.catalog_plan_title" as never);
  return <Modal title={planTitle} className="engine-catalog-operation engine-catalog-plan" onClose={onClose} lockClose={busy}>
    <div className="ui-modal-body engine-operation-body">
      {!hit && <div className="engine-operation-manual">
        {image && <select value={sourceType} onChange={(event) => { setSourceType(event.currentTarget.value as CatalogSource); resetInspection(); }}><option value="civitai">Civitai</option><option value="hf">Hugging Face / URL</option></select>}
        <input value={manualRef} onChange={(event) => changeManualRef(event.currentTarget.value)} placeholder="owner/repository or https://…" />
        <Button small onClick={() => void inspect()} disabled={busy || !manualRef.trim()}>{tr("admin.catalog_inspect" as never)}</Button>
      </div>}
      <div className="engine-operation-grid">
        {!plainURL && <label><span>{tr("admin.catalog_version" as never)}</span><select value={versionRef} onChange={(event) => { const next = event.currentTarget.value; setVersionRef(next); void loadFiles(next); }}>
          {!versions.length && <option value={versionRef}>{versionRef || "—"}</option>}{versions.map((version) => <option key={version.ref} value={version.ref}>{version.name}</option>)}
        </select></label>}
        <label><span>{tr(plainURL ? "admin.catalog_checksum" as never : "admin.catalog_file" as never)}</span>{files.length
          ? <select value={file} onChange={(event) => setFile(event.currentTarget.value)}><option value="">{tr("admin.catalog_pick_file" as never)}</option>{files.map((candidate) => <option key={candidate.ref || candidate.name} value={candidate.name}>{candidate.name}</option>)}</select>
          : <input value={file} onChange={(event) => setFile(event.currentTarget.value)} placeholder={plainURL ? "sha256" : "model.safetensors"} />}</label>
        {asksFamily && <label><span>{tr(isLora && !image ? "admin.engines_model_add_lora_base" : "admin.engines_model_add_family")}</span><select value={baseModel} onChange={(event) => { setBaseModel(event.currentTarget.value); setOperatorFamily(event.currentTarget.value); }}>
          <option value="">{tr(isLora && !image ? "admin.engines_model_add_lora_base_pick" : "admin.engines_model_add_family_pick")}</option>{familyOptions.map((base) => <option key={base} value={base}>{base}</option>)}</select></label>}
      </div>
      {file && !plan && !err && <p className="muted engine-plan-building"><Icon name="loading" spin /> {tr("admin.catalog_plan_building" as never)}</p>}
      {replanned && <p className="admin-hint engine-plan-stale">{tr("admin.catalog_plan_stale" as never)}</p>}
      {plan && <div className="engine-plan">
        <ul className="engine-plan-files" aria-label={tr("admin.catalog_plan_files" as never)}>{plan.files.map((planned) => <li key={`${planned.flag || "whole"}:${planned.name}`}>
          <span className="mono engine-plan-role">{planned.flag || tr("admin.catalog_plan_whole" as never)}</span>
          <span className="mono engine-plan-name">{planned.name}</span>
          {/* A move and a reuse cost nothing, and saying so with a size beside them is what makes
              the total untrustworthy: those bytes are already paid for. */}
          <span className={`engine-plan-cost${planned.action === "download" ? "" : " free"}`}>{planned.action === "download"
            ? (planned.bytes ? `${Math.round(planned.bytes / 1048576)} MiB` : tr("admin.catalog_size_unknown" as never))
            : tr((`admin.catalog_plan_action_${planned.action}`) as never)}</span>
          {/* One field, two meanings, told apart by the action: the upstream for a download and
              the key the bytes are at TODAY for a reuse or a move. Drawn unbranched, "いまの場所"
              would name a repository and a download would claim to come from the bucket. */}
          {planned.source && <span className="muted mono engine-plan-source">{planned.action === "download"
            ? planned.source
            : (tr("admin.catalog_plan_at" as never) as string).replace("{k}", planned.source)}</span>}
        </li>)}</ul>
        <p className="engine-plan-total">{plan.bytes_to_download
          ? (tr("admin.catalog_plan_total" as never) as string).replace("{n}", formatBytes(plan.bytes_to_download))
          : tr("admin.catalog_plan_total_none" as never)}</p>
        {(plan.warnings || []).map((warning) => <p key={warning} className="admin-hint engine-plan-warning">{warning}</p>)}
      </div>}
      {resolved && <div className="engine-operation-facts">
        {(resolved.license_name || resolved.license) && <span>{resolved.license_name || resolved.license}</span>}
        {/* The "no" verdict is deliberately NOT a chip here: it is the red sentence below, and
            saying it twice in two wordings reads as two different restrictions. */}
        {resolved.commercial_use && resolved.commercial_use !== "no" && <span>{tr((`admin.catalog_commercial_${resolved.commercial_use}`) as never)}</span>}
        {resolved.restrictions?.map((code) => <span key={code} className={CATALOG_HARD_RESTRICTIONS.has(code) ? "warn" : ""}>{tMaybe(`admin.engines_limit_${code}`) ?? code}</span>)}
        {resolved.gated && !resolved.restrictions?.length && <span className="warn">{tr("admin.engines_ingest_hit_gated")}</span>}</div>}
      {resolved?.commercial_use === "no" && <p className="form-err">{tr("admin.engines_ingest_noncommercial")}</p>}
      {/* 🔴 The wall is a REFUSAL only while nobody can answer it. With an account registered the
          download is attempted as that account, and the CP cannot say in advance whether it
          satisfies this uploader — the same position `gated_needs_acceptance` is in, so it reads
          as the same kind of warning rather than a red dead end. */}
      {resolved?.login_required && !resolved.civitai_needs_account && <p className="form-err">{tr("admin.engines_ingest_civitai_login")}</p>}
      {resolved?.civitai_needs_account && <p className="muted">{tr("admin.engines_ingest_civitai_account_first" as never)}</p>}
      {resolved?.gated && resolved.can_ingest === false && !resolved.login_required && <p className="form-err">{tr("admin.engines_ingest_gated_no_token")}</p>}
      {resolved?.gated_needs_acceptance && <p className="muted">{tr("admin.engines_ingest_gated_accept_first")}</p>}
      {/* The window, OUT of the advanced fold (ADR 0089). It is the number that decides the
          verdict below — the KV cache is linear in it — and it spent this screen's whole life
          behind a `<summary>` the person reading the red line never opened. */}
      {!image && !isLora && !!resolved && <div className="engine-operation-window">
        <label><span>{tr("admin.engines_model_window_context")}</span>
          <input inputMode="numeric" value={context} onChange={(event) => setContext(event.currentTarget.value)} /></label>
        {!!resolved.context_length && <span className="muted">{(tr("admin.engines_ingest_ctx_ceiling" as never) as string)
          .replace("{n}", resolved.context_length.toLocaleString())}</span>}
      </div>}
      {needMiB > 0 && <p className={`engine-operation-fit ${fit.state === "over" ? "form-err" : "muted"}`}>
        {(tr("admin.engines_ingest_fit_weights") as string).replace("{n}", String(weightsMiB))}
        {!image && !isLora ? ` · ${kvMiB ? (tr("admin.engines_ingest_fit_kv") as string).replace("{n}", String(kvMiB)).replace("{c}", String(contextTokens)) : tr("admin.engines_ingest_fit_kv_unread")}` : ""}
        {cardMiB ? ` · ${(tr("admin.engines_ingest_fit_card") as string).replace("{n}", String(needMiB)).replace("{c}", String(cardMiB))} ` : " "}
        <FitTag fit={fit} />
      </p>}
      {/* Directly under the verdict the files' sum produced, because that sum is what this
          corrects (ADR 0094 decision 8). The press itself is NOT here: nothing the ingest sends
          may write `vram_mib`, or the column stops meaning "the operator measured it" — so this
          says the number and where to put it, and the row's Edit is where it goes in. */}
      {ingestMeasured && <p className="muted engine-operation-vram-measured">
        {(tr("admin.engines_vram_measured" as never) as string)
          .replace("{n}", ingestMeasured.mib.toLocaleString()).replace("{s}", ingestMeasured.size)
          .replace("{b}", String(ingestMeasured.batch)).replace("{i}", String(ingestMeasured.inputs))
        .replace("{f}", ingestMeasured.file).replace("{c}", ingestMeasured.card)}
        {" "}{tr("admin.engines_vram_measured_after" as never)}
      </p>}
      <label className="engine-operation-check"><input type="checkbox" checked={accepted} onChange={(event) => setAccepted(event.currentTarget.checked)} /><span>{tr("admin.engines_ingest_accept")}</span></label>
      <details className="engine-operation-advanced"><summary>{tr("admin.catalog_advanced" as never)}</summary><div className="engine-operation-grid">
        <label><span>{tr("admin.engines_model_add_id")}</span><input value={id} onChange={(event) => { setIdEdited(true); setId(event.currentTarget.value); }} /></label>
        <label><span>{tr("admin.engines_model_add_desc")}</span><input value={description} onChange={(event) => setDescription(event.currentTarget.value)} /></label>
        {!image && !isLora && <label><span>{tr("admin.engines_model_add_out")}</span><input value={output} onChange={(event) => setOutput(event.currentTarget.value)} inputMode="numeric" /></label>}
        {isLora && <label><span>{tr("admin.engines_model_trigger")}</span><input value={trainedWords} onChange={(event) => setTrainedWords(event.currentTarget.value)} /></label>}
        {image && (Object.keys(params) as (keyof EngineParams)[]).map((key) => <label key={key}><span>{tr((`admin.engines_params_${key}`) as never)}</span><input value={params[key]} onChange={(event) => setParams((current) => ({ ...current, [key]: event.currentTarget.value }))} /></label>)}
      </div></details>
      <EngineRefusal error={err} busy={busy} onNext={() => { setErr(null); void inspect(); }} />
      <footer className="engine-operation-footer"><span className="muted">{missing}</span><Button variant="ghost" onClick={onClose} disabled={busy}>{tr("common.cancel")}</Button><Button variant="primary" onClick={() => void start()} disabled={busy || !!missing}>{tr("admin.engines_ingest_go")}</Button></footer>
    </div>
  </Modal>;
}
