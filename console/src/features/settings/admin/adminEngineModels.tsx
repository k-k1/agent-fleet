import { useState } from "react";
import { apiJSON, errDetail } from "../../../core/api/client.ts";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime } from "../../../lib/intl.ts";
import { type IngestHit } from "./engineTypes.ts";

// What there is to run, on a deployment that has no engine to run it on.
//
// Everything a deployment WITH an engine does to its catalogue — search, take in, enable, 揃える,
// the bucket itself — is the catalogue pane (adminEngineAdd.tsx, ADR 0085 decisions 4 and 8).
// This file is what is left once that pane owns the subject: the upstream browser, which needs
// no engine at all (ADR 0072 decision 11) and therefore cannot live on a screen that starts from
// one.
//
// 🔴 It has exactly one caller shape: a deployment whose engine list is EMPTY. Both callers
// (adminEngines.tsx and adminEngineCatalogLauncher.tsx) gate on that, and with a row present the
// launcher opens the pane instead — so nothing here reads an engine, a bucket or a job.

/** Which half of a catalogue is on screen. A LoRA is an accessory rather than a model — never
 *  what an engine starts with, and pinned to a FAMILY rather than to a checkpoint — so the two
 *  lists answer different questions and the tab decides which one is being asked. */
export type ModelKind = "model" | "lora";

/** The engine-less catalogue screen: "there is no engine here" and what could be run on one.
 *
 * 🔴 "There is nothing here" is the worst possible answer to "what could I run?". Looking at what
 * Hugging Face and Civitai hold needs no engine — no token, no bucket, no task — so this screen
 * is useful on a deployment that has adopted nothing, and it says what it cannot do rather than
 * pretending (ADR 0072 decision 11). */
export function EngineModelsAdminView() {
  const tr = useT();
  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <p className="muted">{tr("admin.engines_none")}</p>
        <EngineBrowse />
      </section>
    </div>
  );
}

/** Looking at what there is to stage, on a deployment with no engine to stage it INTO.
 *
 * The same read as the catalogue pane's search with no engine in the path: the CP holds no token,
 * reads no bucket and starts no task to answer it, so nothing about it needs 60-engines to be
 * deployed. What it cannot do is take anything in, and the note says so — an administrator
 * deciding whether self-hosted inference is worth standing up is exactly the person who cannot
 * see the catalogue today. */
function EngineBrowse() {
  const tr = useT();
  const [q, setQ] = useState("");
  const [kind, setKind] = useState("gguf");
  const [sort, setSort] = useState("downloads");
  const [source, setSource] = useState("hf");
  const [hits, setHits] = useState<IngestHit[] | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const run = async (opts: { kind?: string; sort?: string; source?: string } = {}) => {
    setBusy(true);
    setErr("");
    try {
      const k = opts.kind ?? kind;
      const d = await apiJSON(`api/admin/engines/search?kind=${encodeURIComponent(k)}`, "POST", {
        q,
        source: opts.source ?? source,
        sort: opts.sort ?? sort,
      });
      if (d?.error) {
        setHits(null);
        setErr(errDetail(d.error));
        return;
      }
      setHits((Array.isArray(d?.hits) ? d.hits : []) as IngestHit[]);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="engines-model-add engines-ingest">
      <label className="engines-search-row">
        <span>{tr("admin.engines_ingest_search")}</span>
        <input
          value={q}
          placeholder={kind === "gguf" ? "qwen2.5 coder" : "sdxl"}
          onChange={(ev) => setQ(ev.currentTarget.value)}
          onKeyDown={(ev) => {
            if (ev.key === "Enter") {
              ev.preventDefault();
              run();
            }
          }}
        />
      </label>
      <div className="engines-model-add-actions">
        {/* With no engine there is nothing to derive the kind from, so it is asked. */}
        <span className="seg sm">
          {(["gguf", "checkpoint"] as const).map((k) => (
            <button
              key={k}
              type="button"
              className={"seg-btn" + (kind === k ? " active" : "")}
              onClick={() => {
                setKind(k);
                setHits(null);
                // Civitai hosts image models only, so switching to GGUF has to take the source
                // back with it — otherwise the next search is Civitai-for-LLM, which the CP
                // correctly answers with nothing and which reads as a broken search.
                if (k === "gguf") setSource("hf");
              }}
            >
              {tr(("admin.engines_browse_kind_" + k) as never)}
            </button>
          ))}
        </span>
        {/* Civitai only for checkpoints — the same rule the catalogue pane's search follows. */}
        {kind === "checkpoint" && (
          <span className="seg sm">
            {(["hf", "civitai", "civitai-red"] as const).map((sr) => (
              <button
                key={sr}
                type="button"
                className={"seg-btn" + (source === sr ? " active" : "")}
                onClick={() => {
                  setSource(sr);
                  setHits(null);
                }}
              >
                {tr(("admin.engines_ingest_source_" + sr) as never)}
              </button>
            ))}
          </span>
        )}
        <span className="seg sm">
          {(["downloads", "trending", "likes"] as const).map((sr) => (
            <button
              key={sr}
              type="button"
              className={"seg-btn" + (sort === sr ? " active" : "")}
              onClick={() => {
                setSort(sr);
                run({ sort: sr });
              }}
            >
              {tr(("admin.engines_ingest_sort_" + sr) as never)}
            </button>
          ))}
        </span>
        <button type="button" className="primary sm" onClick={() => run()} disabled={busy}>
          {q.trim() ? tr("admin.engines_ingest_search_go") : tr("admin.engines_ingest_browse_go")}
        </button>
      </div>
      {hits && hits.length === 0 && <p className="muted">{tr("admin.engines_ingest_search_none")}</p>}
      {hits && hits.length > 0 && (
        <ul className="engines-search-hits">
          {/* No pick button: there is nowhere to put it. Picking one opens a plan, and this
              deployment has no role to take anything into. */}
          {hits.map((h) => (
            <HitCard key={h.source + ":" + h.ref} hit={h} />
          ))}
        </ul>
      )}
      {err && <p className="form-err">{err}</p>}
      <p className="muted">{tr("admin.engines_browse_note")}</p>
    </div>
  );
}

/** One search result, as a card.
 *
 * The three kinds of fact are told apart by KIND rather than by a separator character — what
 * it is called, the numbers a ranking is built on, the terms it comes with — because twenty
 * results as one "・"-joined line each are a wall of text with nothing to scan by.
 *
 * Every part is omitted rather than guessed: the two APIs answer different subsets, and a zero
 * download count reads as a fact.
 *
 * The gating flag and the licence stay on the card rather than moving behind a detail view.
 * They decide whether this row is takeable at all, and learning that from a refusal one step
 * later is the dead end the whole picker exists to avoid. */
/** Date only, with the year: a search result's dates are months or years old, and the default
 *  "M/D HH:MM" of fmtDateTime would print a 2024 model as if it were this year. */
const HIT_DATE: Intl.DateTimeFormatOptions = { year: "numeric", month: "numeric", day: "numeric" };

/** The word for one restriction code. The codes are the CP's closed set (engineRestrict* in
 *  engine_ingest_limits.go) and the words live only here, because the two sources spell the same
 *  restriction differently and this is where the locale catalogue is.
 *
 *  An unknown code is printed VERBATIM rather than dropped: a Control Plane newer than this
 *  bundle is exactly the case where a restriction nobody here has heard of matters most. */
function engineRestrictLabel(code: string): string {
  return tMaybe("admin.engines_limit_" + code) ?? code;
}

/** Which restrictions stop the download rather than limit what may be done with it. They are
 *  drawn in the warning colour: one of them means choosing this row is choosing a refusal, and
 *  the other kind is a decision for a human to make. */
const ENGINE_HARD_LIMITS = new Set([
  "gated_auto",
  "gated_manual",
  "paid",
  "early_access",
  "private",
  "generate_only",
  "unscanned",
  "pickle",
]);

function engineRestrictIsHard(code: string): boolean {
  return ENGINE_HARD_LIMITS.has(code);
}

function HitCard({ hit }: { hit: IngestHit }) {
  const tr = useT();
  const stat = (n: number | undefined, key: string) =>
    n ? (
      <span className="engines-hit-stat">
        <b>{fmtCount(n)}</b>
        {(tr(key as never) as string).trim()}
      </span>
    ) : null;
  /** Published and last-updated, as ONE flex item rather than two.
   *
   * 🔴 Measured on the real bundle (ja, the modal's width): the busiest card's strip is 390px
   * and its three counts plus two separate dates come to 392 — two over, so the pair split
   * across a line break and the card grew 118px → 146px. Kept together they are 382 and the
   * line holds; when a locale's labels are wider they move down as a pair, which is the
   * legible way to lose the race. */
  const dates = [
    { k: "published", iso: hit.published_at, label: "admin.engines_ingest_hit_published" },
    { k: "updated", iso: hit.updated_at, label: "admin.engines_ingest_hit_updated" },
  ].filter((d) => !!d.iso);
  const lic = hit.license_name || hit.license;
  return (
    <li className="engines-hit">
      <div className="engines-hit-head">
        <span className="mono engines-hit-name">{hit.name}</span>
        {/* The page this row came from. The href is the CP's string as it stands — building it
            here would mean the panel learning both sources' spellings, and Civitai's needs an
            id this row does not carry. stopPropagation because the card is the "choose this
            result" surface: a link that also chose would send somebody two places at once. */}
        {hit.url && (
          <a
            className="engines-hit-link"
            href={hit.url}
            target="_blank"
            rel="noopener noreferrer"
            onClick={(ev) => ev.stopPropagation()}
          >
            {tr(hit.source === "civitai" ? "admin.engines_ingest_hit_open_civitai" : "admin.engines_ingest_hit_open_hf")}
          </a>
        )}
      </div>
      <div className="engines-hit-stats">
        {stat(hit.downloads, "admin.engines_ingest_hit_downloads")}
        {stat(hit.likes, "admin.engines_ingest_hit_likes")}
        {stat(hit.trending, "admin.engines_ingest_hit_trending")}
        {/* The pair, in the same row as the counts and each labelled: "published in 2024, last
            touched last week" and "published last week" are different models to choose
            between, and one date alone says neither. Absent is absent — Civitai publishes no
            update date at all, and an empty label would read as "never". */}
        {dates.length > 0 && (
          <span className="engines-hit-stat engines-hit-date">
            {dates.map((d) => (
              <span key={d.k}>
                {(tr(d.label as never) as string).trim()} <b>{fmtDateTime(d.iso as string, HIT_DATE)}</b>
              </span>
            ))}
          </span>
        )}
      </div>
      <div className="engines-hit-tags">
        {/* 🔴 What would REFUSE this row comes first and in its own colour, because it is the
            only kind of tag that turns a choice into a wasted nine-minute download.
            "Login required" leads even that: it is the one restriction no token on this
            deployment can satisfy, and measured 2026-09-12 it is true of 13 of the 20 rows
            Civitai's own ranking puts on the first screen. Absent means the CP could not tell
            — never "anyone may have it" — so nothing is drawn for it. */}
        {hit.login_required === "yes" && (
          <span className="engines-model-tag warn" title={tr("admin.engines_hit_login_note")}>
            {tr("admin.engines_hit_login_required")}
          </span>
        )}
        {/* Civitai's own content rating. Drawn on every Civitai hit, not only ones from the
            civitai-red tab — the plain tab's own default query still answers a nonzero level
            (measured), so a card without this would read as "safe" on a false premise. */}
        {!!hit.nsfw_level && (
          <span className="engines-model-tag">
            {(tr("admin.engines_ingest_hit_nsfw_level" as never) as string).replace("{n}", String(hit.nsfw_level))}
          </span>
        )}
        {/* The gate, in the two flavours Hugging Face publishes. The bare `gated` stays as the
            fallback for a CP too old to send which kind, so this panel never loses the warning
            it has had since P4 just because the field it now prefers is missing. */}
        {hit.restrictions?.length
          ? hit.restrictions.map((code) => (
              <span key={code} className={"engines-model-tag" + (engineRestrictIsHard(code) ? " warn" : "")}>
                {engineRestrictLabel(code)}
              </span>
            ))
          : hit.gated && (
              <span className="engines-model-tag warn">{tr("admin.engines_ingest_hit_gated")}</span>
            )}
        {lic && <span className="engines-model-tag">{lic}</span>}
        {hit.base_model && <span className="engines-model-tag">{hit.base_model}</span>}
        {/* A LoRA does nothing without its trigger, and this is the only place the words are
            published in a form that can be copied rather than read off a picture. */}
        {hit.trained_words?.length ? (
          <span className="engines-model-tag">
            {(tr("admin.engines_hit_trigger") as string) + ": " + hit.trained_words.join(", ")}
          </span>
        ) : null}
        {!!hit.bytes && <span className="engines-model-tag">{fmtBytes(hit.bytes)}</span>}
        {!!hit.context_length && (
          <span className="engines-model-tag">
            {(tr("admin.engines_ingest_ctx_max" as never) as string).replace("{n}", String(hit.context_length))}
          </span>
        )}
      </div>
    </li>
  );
}

/** 1,632,949 → 1.6M. The exact number is noise next to "is this the one everybody uses".
 *
 * 🔴 The small end is rounded because one of these numbers is a SCORE, not a count: Hugging
 * Face's `trendingScore` comes back fractional, and 0.7000000000000001 is what a raw
 * `String(n)` puts on the row. */
function fmtCount(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1).replace(/\.0$/, "") + "M";
  if (n >= 1_000) return Math.round(n / 1_000) + "k";
  return String(Math.round(n * 10) / 10);
}

function fmtBytes(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(1) + " GB";
  if (n >= 1e6) return Math.round(n / 1e6) + " MB";
  return n + " B";
}
