// BackupTab — settings export / import (docs/log/79 / ADR 0060).
//
// Carries one person's settings out as a single JSON file so they can be read back on
// another deployment, another tenant or a new account. Only three secret-free layers are
// carried: personal settings (what ui-prefs sync covers), AWS SSM (profiles and hosts) and
// the user instructions. Connection tokens are deliberately excluded: the file is plaintext
// and meant to be shared casually as "just a settings file", and a single secret in it would
// make the whole artefact something that has to be handled carefully.
//
// Import rules:
//   1. Add only. Existing profiles / hosts are never overwritten (same name or same instance
//      is skipped, with the reason reported).
//   2. Never flatten accumulated data with an empty value. Personal settings are merged onto
//      the current ones (mergeImportedPrefs in settingsBundle.ts), so import cannot reopen the
//      hole that a whole-object PUT sync did when it wiped every device's learned state.
//   3. Never silence what did not go in. Version mismatches, type mismatches and hosts whose
//      referent is missing are surfaced as counts.
import { useCallback, useEffect, useRef, useState } from "react";
import { api, errDetail, rawJSON } from "../../../core/api/client.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useWorkspaceStore } from "../../../core/store/workspace.ts";
import {
  getSettings,
  isAccumulatedSetting,
  setSettings,
  settingsDefaults,
  type Settings,
} from "../../../lib/settings.ts";
import {
  bundleFileName,
  buildBundle,
  exportablePrefs,
  mergeImportedPrefs,
  parseBundle,
  planSsmImport,
  profileIdByLabel,
  sanitizeImportedPrefs,
  summarizeBundle,
  toInstructionsSection,
  toSsmSection,
  utf8Bytes,
  type BundleSections,
  type ParseError,
  type SectionKey,
  type SettingsBundle,
} from "../../../lib/settingsBundle.ts";
import { tCount, useT } from "../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";

type Picked = Record<SectionKey, boolean>;

interface Loaded {
  profiles: any[];
  hosts: any[];
  instructions: any | null;
}

export function BackupTab() {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const running = useWorkspaceStore((s) => s.state) === "running";
  const fileRef = useRef<HTMLInputElement>(null);

  const [loaded, setLoaded] = useState<Loaded>({ profiles: [], hosts: [], instructions: null });
  const [busy, setBusy] = useState(false);
  const [exportPick, setExportPick] = useState<Picked>({ prefs: true, ssm: true, instructions: true });
  const [bundle, setBundle] = useState<SettingsBundle | null>(null);
  const [importPick, setImportPick] = useState<Picked>({ prefs: true, ssm: true, instructions: true });
  const [result, setResult] = useState<string[] | null>(null);

  const reload = useCallback(async () => {
    const [profiles, hosts] = await Promise.all([api("api/ssm/profiles"), api("api/ssm/hosts")]);
    // The user instructions live in the Agent, so they are readable only while the workspace
    // is running. When it is stopped, drop the whole category so the UI can show it is absent
    // rather than silently exporting it as empty.
    const instructions = running ? await api("api/user-notes") : null;
    setLoaded({
      profiles: Array.isArray(profiles) ? profiles : [],
      hosts: Array.isArray(hosts) ? hosts : [],
      instructions: instructions && !instructions.error ? instructions : null,
    });
  }, [running]);
  useEffect(() => {
    void reload();
  }, [reload]);

  const prefsCount = Object.keys(exportablePrefs(getSettings() as any, settingsDefaults() as any)).length;
  const instrBytes = utf8Bytes(loaded.instructions?.text ?? "");
  const canExport = exportPick.prefs || exportPick.ssm || (exportPick.instructions && !!loaded.instructions);

  const doExport = () => {
    const sections: BundleSections = {};
    if (exportPick.prefs) sections.prefs = exportablePrefs(getSettings() as any, settingsDefaults() as any);
    if (exportPick.ssm) sections.ssm = toSsmSection(loaded.profiles, loaded.hosts);
    if (exportPick.instructions && loaded.instructions) {
      sections.instructions = toInstructionsSection(loaded.instructions);
    }
    const now = new Date();
    const text = JSON.stringify(buildBundle(sections, now.toISOString()), null, 2);
    const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = bundleFileName(now);
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 10_000);
    toast(tr("backup.export_done"), { kind: "success" });
  };

  const readFile = async (file: File) => {
    setResult(null);
    setBundle(null);
    const text = await file.text();
    const parsed = parseBundle(text);
    if ("error" in parsed) {
      toast(tr(("backup.err_" + parsed.error) as `backup.err_${ParseError}`));
      if (fileRef.current) fileRef.current.value = "";
      return;
    }
    const s = parsed.bundle.sections;
    setImportPick({ prefs: !!s.prefs, ssm: !!s.ssm, instructions: !!s.instructions });
    setBundle(parsed.bundle);
    if (fileRef.current) fileRef.current.value = ""; // so the same file can be picked again
  };

  const applyImport = async () => {
    if (!bundle) return;
    const s = bundle.sections;
    const wantPrefs = importPick.prefs && !!s.prefs;
    const wantSsm = importPick.ssm && !!s.ssm;
    const wantInstr = importPick.instructions && !!s.instructions;
    if (!wantPrefs && !wantSsm && !wantInstr) return;
    const ok = await askConfirm({
      title: tr("backup.confirm_title"),
      body: (
        <>
          <p>{tr("backup.confirm_body")}</p>
          {wantInstr && <p className="warn-text">{tr("backup.confirm_instructions")}</p>}
        </>
      ),
      confirmLabel: tr("backup.import_do"),
    });
    if (!ok) return;

    setBusy(true);
    const lines: string[] = [];
    try {
      if (wantPrefs && s.prefs) {
        const { patch, skipped } = sanitizeImportedPrefs(s.prefs, settingsDefaults() as any);
        const merged = mergeImportedPrefs(getSettings() as any, patch, (k) =>
          isAccumulatedSetting(k as keyof Settings),
        );
        setSettings(merged as Partial<Settings>);
        lines.push(
          tr("backup.res_prefs", { applied: Object.keys(merged).length, skipped: skipped.length }),
        );
      }
      if (wantSsm && s.ssm) {
        lines.push(...(await importSsm(s.ssm, tr)));
      }
      if (wantInstr && s.instructions) {
        const res = await rawJSON("api/user-notes", "PUT", {
          text: s.instructions.text,
          enabled: s.instructions.enabled,
          targets: s.instructions.targets,
        });
        if (res.ok) {
          lines.push(tr("backup.res_instructions", { bytes: utf8Bytes(s.instructions.text) }));
        } else {
          const body = await res.json().catch(() => null);
          lines.push(tr("backup.res_instructions_failed", { msg: body?.error ? errDetail(body.error) : String(res.status) }));
        }
      }
      setResult(lines);
      setBundle(null);
      toast(tr("backup.import_done"), { kind: "success", persist: true });
      await reload();
    } finally {
      setBusy(false);
    }
  };

  const summary = bundle ? summarizeBundle(bundle) : null;

  return (
    <div className="display-settings backup-tab">
      <p className="field-help">{tr("backup.intro")}</p>
      <p className="field-help">{tr("backup.secrets_note")}</p>

      <section className="ds-group">
        <h4 className="ds-title">{tr("backup.export_title")}</h4>
        <ul className="backup-picks">
          <Pick
            on={exportPick.prefs}
            onChange={(v) => setExportPick((p) => ({ ...p, prefs: v }))}
            label={tr("backup.cat_prefs")}
            note={tCount("backup.n_keys", prefsCount)}
          />
          <Pick
            on={exportPick.ssm}
            onChange={(v) => setExportPick((p) => ({ ...p, ssm: v }))}
            label={tr("backup.cat_ssm")}
            note={tr("backup.n_ssm", { profiles: loaded.profiles.length, hosts: loaded.hosts.length })}
          />
          <Pick
            on={exportPick.instructions && !!loaded.instructions}
            disabled={!loaded.instructions}
            onChange={(v) => setExportPick((p) => ({ ...p, instructions: v }))}
            label={tr("backup.cat_instructions")}
            note={loaded.instructions ? tCount("backup.n_bytes", instrBytes) : tr("backup.needs_ws")}
          />
        </ul>
        <div className="flow">
          <button type="button" className="primary" disabled={!canExport} onClick={doExport}>
            {tr("backup.export_do")}
          </button>
        </div>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("backup.import_title")}</h4>
        <input
          ref={fileRef}
          type="file"
          className="cinput"
          accept="application/json,.json"
          disabled={busy}
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void readFile(f);
          }}
        />
        <p className="field-help">{tr("backup.import_hint")}</p>

        {bundle && summary && (
          <div className="backup-preview">
            <p className="muted">{tr("backup.preview_head", {
                when: bundle.exportedAt ? fmtDateTime(bundle.exportedAt, DATETIME_FULL) : "-",
              })}</p>
            <ul className="backup-picks">
              {bundle.sections.prefs && (
                <Pick
                  on={importPick.prefs}
                  onChange={(v) => setImportPick((p) => ({ ...p, prefs: v }))}
                  label={tr("backup.cat_prefs")}
                  note={tCount("backup.n_keys", summary.prefs)}
                />
              )}
              {bundle.sections.ssm && (
                <Pick
                  on={importPick.ssm}
                  onChange={(v) => setImportPick((p) => ({ ...p, ssm: v }))}
                  label={tr("backup.cat_ssm")}
                  note={tr("backup.n_ssm", { profiles: summary.profiles, hosts: summary.hosts })}
                />
              )}
              {bundle.sections.instructions && (
                <Pick
                  on={importPick.instructions && running}
                  disabled={!running}
                  onChange={(v) => setImportPick((p) => ({ ...p, instructions: v }))}
                  label={tr("backup.cat_instructions")}
                  note={running ? tCount("backup.n_bytes", summary.instructionBytes) : tr("backup.needs_ws")}
                />
              )}
            </ul>
            <div className="flow">
              <button type="button" className="primary" disabled={busy} onClick={() => void applyImport()}>
                {tr("backup.import_do")}
              </button>
              <button type="button" className="ghost" onClick={() => setBundle(null)}>
                {tr("common.cancel")}
              </button>
            </div>
          </div>
        )}

        {result && (
          <ul className="backup-result">
            {result.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function Pick({
  on,
  disabled,
  onChange,
  label,
  note,
}: {
  on: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  label: string;
  note: string;
}) {
  return (
    <li>
      <label>
        <input type="checkbox" checked={on} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
        {label}
        <span className="muted"> {note}</span>
      </label>
    </li>
  );
}

// importSsm — create the profiles first, then the hosts that reference their ids. The order
// is essential, so this is not a Promise.all: a host whose referent does not exist yet is a 400.
async function importSsm(
  section: NonNullable<BundleSections["ssm"]>,
  tr: (k: any, v?: any) => string,
): Promise<string[]> {
  const [curProfiles, curHosts] = await Promise.all([api("api/ssm/profiles"), api("api/ssm/hosts")]);
  const plan = planSsmImport(
    section,
    Array.isArray(curProfiles) ? curProfiles : [],
    Array.isArray(curHosts) ? curHosts : [],
  );
  const ids = profileIdByLabel(Array.isArray(curProfiles) ? curProfiles : []);
  let addedProfiles = 0;
  let failed = 0;
  for (const p of plan.profiles) {
    const res = await rawJSON("api/ssm/profiles", "POST", p);
    if (!res.ok) {
      failed++;
      continue;
    }
    const created = await res.json().catch(() => null);
    if (created?.id) ids.set(p.label.trim().toLowerCase(), created.id);
    addedProfiles++;
  }
  let addedHosts = 0;
  for (const h of plan.hosts) {
    const profileId = ids.get(h.profile.trim().toLowerCase());
    if (!profileId) {
      failed++;
      continue;
    }
    const res = await rawJSON("api/ssm/hosts", "POST", {
      alias: h.alias,
      profileId,
      instanceId: h.instanceId,
      documentName: h.documentName,
      region: h.region,
    });
    if (!res.ok) {
      failed++;
      continue;
    }
    addedHosts++;
  }
  const skipped = plan.skippedProfiles.length + plan.skippedHosts.length;
  const lines = [
    tr("backup.res_ssm", { profiles: addedProfiles, hosts: addedHosts, skipped }),
  ];
  if (failed > 0) lines.push(tr("backup.res_ssm_failed", { n: failed }));
  return lines;
}
