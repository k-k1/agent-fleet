// 設定の書き出し / 取り込み（docs/log/79 / ADR 0060）のデータ層。
//
// 1 個の JSON（バンドル）に、その人の設定のうち **秘密を含まない層** だけを詰める:
//   prefs        … Console の個人設定（ui-prefs 同期の対象そのもの）
//   ssm          … AWS SSM のプロファイル / ホスト（CP の DB・メンバー単位）
//   instructions … ユーザー指示（~/.config/agent-fleet/user-notes.md）
// 接続（Git / エージェント / AWS のトークン類）は **入れない**。バンドルはメールや
// チャットで運ばれる前提の平文なので、秘密が 1 つでも混ざると全体の扱いが変わる。
//
// ここは純ロジックだけ（fetch も React も import しない）。設定の既定値と累積キーの
// 判定は呼び手（settings.ts）から渡してもらう —— settings.ts は localStorage に触る
// ので node のテストから import できず、混ぜると全部 DOM テストになるため。
//
// 設計上の要点 2 つ:
//   ① **ホストはプロファイルを「表示名」で参照する。** CP の id は環境ごとに違うので、
//      id のまま運ぶと取り込み先で必ず張り替えが要る。表示名は ~/.aws のプロファイル名
//      の素でもあり、人が見て分かる自然キーなので、形式の側で id 問題を消す。
//   ② **取り込みは足すだけ。** 既にあるものは触らない（同名プロファイル・同じ
//      alias+instance のホスト）。設定の移送で既存環境を削るのは割に合わない。

export const BUNDLE_KIND = "agent-fleet-settings";
export const BUNDLE_VERSION = 1;

export type SectionKey = "prefs" | "ssm" | "instructions";
export const SECTION_KEYS: SectionKey[] = ["prefs", "ssm", "instructions"];

export interface SsmProfileEntry {
  label: string;
  startUrl: string;
  ssoRegion: string;
  accountId: string;
  roleName: string;
  region: string;
}

export interface SsmHostEntry {
  alias: string;
  /** 参照するプロファイルの表示名（id ではない — 上記②）。 */
  profile: string;
  instanceId: string;
  documentName: string;
  region: string;
}

export interface SsmSection {
  profiles: SsmProfileEntry[];
  hosts: SsmHostEntry[];
}

export interface InstructionsSection {
  text: string;
  enabled: boolean;
  targets: Record<string, boolean>;
}

export interface BundleSections {
  prefs?: Record<string, unknown>;
  ssm?: SsmSection;
  instructions?: InstructionsSection;
}

export interface SettingsBundle {
  kind: string;
  version: number;
  exportedAt: string;
  sections: BundleSections;
}

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");
const key = (v: string): string => v.trim().toLowerCase();

// --- 書き出し -------------------------------------------------------------------

/** 既知のキーだけを持つ設定の浅いコピー。古い Console が書いた見知らぬキーは落とす
 *  （取り込み側でどうせ弾かれるので、運ぶ意味がない）。 */
export function exportablePrefs(
  state: Record<string, unknown>,
  defaults: Record<string, unknown>,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(defaults)) {
    if (k in state) out[k] = state[k];
  }
  return out;
}

/** CP の DTO（id 参照）をバンドルの形（表示名参照）へ移す。参照先が見つからない
 *  ホストは profile が空のまま入る＝取り込み側で理由付きスキップになる。 */
export function toSsmSection(profiles: any[], hosts: any[]): SsmSection {
  const labelOf = new Map<string, string>();
  for (const p of profiles || []) labelOf.set(String(p?.id ?? ""), str(p?.label));
  return {
    profiles: (profiles || []).map((p) => ({
      label: str(p?.label),
      startUrl: str(p?.startUrl),
      ssoRegion: str(p?.ssoRegion),
      accountId: str(p?.accountId),
      roleName: str(p?.roleName),
      region: str(p?.region),
    })),
    hosts: (hosts || []).map((h) => ({
      alias: str(h?.alias),
      profile: labelOf.get(String(h?.profileId ?? "")) ?? "",
      instanceId: str(h?.instanceId),
      documentName: str(h?.documentName),
      region: str(h?.region),
    })),
  };
}

/** user-notes の GET 応答（targets は配列）をバンドルの形（kind → ON/OFF）へ。 */
export function toInstructionsSection(payload: any): InstructionsSection {
  const targets: Record<string, boolean> = {};
  for (const t of payload?.targets || []) {
    if (t && typeof t.kind === "string" && t.supported) targets[t.kind] = t.on === true;
  }
  return {
    text: typeof payload?.text === "string" ? payload.text : "",
    enabled: payload?.enabled !== false,
    targets,
  };
}

export function buildBundle(sections: BundleSections, exportedAt: string): SettingsBundle {
  return { kind: BUNDLE_KIND, version: BUNDLE_VERSION, exportedAt, sections };
}

/** 書き出しファイル名（af-settings-YYYYMMDD-HHmm.json）。時刻はローカル。 */
export function bundleFileName(at: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return (
    "af-settings-" +
    at.getFullYear() +
    p(at.getMonth() + 1) +
    p(at.getDate()) +
    "-" +
    p(at.getHours()) +
    p(at.getMinutes()) +
    ".json"
  );
}

// --- 読み取り -------------------------------------------------------------------

export type ParseError = "bad_json" | "bad_kind" | "bad_version" | "empty";

/** 受け取った JSON をバンドルとして読む。失敗は理由コードで返す（文言は呼び手）。 */
export function parseBundle(text: string): { bundle: SettingsBundle } | { error: ParseError } {
  let raw: any;
  try {
    raw = JSON.parse(text);
  } catch {
    return { error: "bad_json" };
  }
  if (!raw || typeof raw !== "object" || raw.kind !== BUNDLE_KIND) return { error: "bad_kind" };
  // 版は前方互換にしない。知らない版を「知っているつもり」で部分適用すると、
  // 何が入って何が入らなかったのかを利用者が確かめる手段が無くなる。
  if (raw.version !== BUNDLE_VERSION) return { error: "bad_version" };
  const src = raw.sections && typeof raw.sections === "object" ? raw.sections : {};
  const sections: BundleSections = {};
  if (src.prefs && typeof src.prefs === "object" && !Array.isArray(src.prefs)) {
    sections.prefs = src.prefs as Record<string, unknown>;
  }
  if (src.ssm && typeof src.ssm === "object") {
    sections.ssm = {
      profiles: Array.isArray(src.ssm.profiles) ? src.ssm.profiles : [],
      hosts: Array.isArray(src.ssm.hosts) ? src.ssm.hosts : [],
    };
  }
  if (src.instructions && typeof src.instructions === "object") {
    const t = src.instructions.targets;
    sections.instructions = {
      text: typeof src.instructions.text === "string" ? src.instructions.text : "",
      enabled: src.instructions.enabled !== false,
      targets: t && typeof t === "object" && !Array.isArray(t) ? (t as Record<string, boolean>) : {},
    };
  }
  if (!sections.prefs && !sections.ssm && !sections.instructions) return { error: "empty" };
  return { bundle: { kind: raw.kind, version: raw.version, exportedAt: str(raw.exportedAt), sections } };
}

// --- 取り込み（個人設定） --------------------------------------------------------

/** 既定値と「型が合う」既知のキーだけを残す。型が合わない値（他版の Console や手編集）を
 *  そのまま state に入れると、読む側が一斉に壊れるので落とす。 */
export function sanitizeImportedPrefs(
  raw: Record<string, unknown>,
  defaults: Record<string, unknown>,
): { patch: Record<string, unknown>; skipped: string[] } {
  const patch: Record<string, unknown> = {};
  const skipped: string[] = [];
  for (const [k, v] of Object.entries(raw || {})) {
    if (!(k in defaults)) {
      skipped.push(k);
      continue;
    }
    if (!sameShape(defaults[k], v)) {
      skipped.push(k);
      continue;
    }
    patch[k] = v;
  }
  return { patch, skipped };
}

function sameShape(def: unknown, v: unknown): boolean {
  if (Array.isArray(def)) return Array.isArray(v);
  if (def === null) return true; // 既定が null のキーは形を決めない
  if (typeof def === "object") return !!v && typeof v === "object" && !Array.isArray(v);
  return typeof v === typeof def;
}

/** 取り込む値を現在値へ重ねる。累積データ（学習済み候補・キー割当・作業グループ…）は
 *  **空で潰さない／オブジェクトは足し算**にする —— まるごと PUT の同期で全端末の
 *  累積が消えた事故（settings.ts の prefsLoaded のコメント）と同じ穴を、取り込みという
 *  一発操作で開けないため。配列の累積（返信候補など）は中身の同一性を判定できないので
 *  置き換えるが、空の配列では置き換えない。 */
export function mergeImportedPrefs(
  current: Record<string, unknown>,
  patch: Record<string, unknown>,
  isAccumulated: (key: string) => boolean,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(patch)) {
    if (!isAccumulated(k)) {
      out[k] = v;
      continue;
    }
    if (isEmptyValue(v)) continue; // 空で上書きしない
    const cur = current[k];
    if (isPlainObject(v) && isPlainObject(cur)) {
      out[k] = { ...(cur as object), ...(v as object) };
      continue;
    }
    out[k] = v;
  }
  return out;
}

function isPlainObject(v: unknown): boolean {
  return !!v && typeof v === "object" && !Array.isArray(v);
}

function isEmptyValue(v: unknown): boolean {
  if (v == null || v === "") return true;
  if (Array.isArray(v)) return v.length === 0;
  if (typeof v === "object") return Object.keys(v as object).length === 0;
  return false;
}

// --- 取り込み（SSM） -------------------------------------------------------------

export type SkipReason = "exists" | "invalid" | "no_profile";

export interface SsmPlan {
  /** 新規に作るプロファイル。 */
  profiles: SsmProfileEntry[];
  /** 新規に作るホスト（profile は表示名のまま。id は作成後に解決する）。 */
  hosts: SsmHostEntry[];
  skippedProfiles: { label: string; reason: SkipReason }[];
  skippedHosts: { alias: string; reason: SkipReason }[];
}

/** 取り込み結果を「今ある物」と突き合わせて、実際に作る分だけに絞る。
 *  プロファイルは表示名、ホストは alias + インスタンス id が同じなら既存とみなす。 */
export function planSsmImport(
  section: SsmSection,
  existingProfiles: any[],
  existingHosts: any[],
): SsmPlan {
  const plan: SsmPlan = { profiles: [], hosts: [], skippedProfiles: [], skippedHosts: [] };
  const haveProfile = new Set((existingProfiles || []).map((p) => key(str(p?.label))));
  const haveHost = new Set(
    (existingHosts || []).map((h) => key(str(h?.alias)) + "\u0000" + key(str(h?.instanceId))),
  );
  // 取り込み後に参照できるプロファイル名 = 既存 ∪ これから作る分。
  const willHave = new Set(haveProfile);
  for (const raw of section.profiles || []) {
    const p: SsmProfileEntry = {
      label: str(raw?.label),
      startUrl: str(raw?.startUrl),
      ssoRegion: str(raw?.ssoRegion),
      accountId: str(raw?.accountId),
      roleName: str(raw?.roleName),
      region: str(raw?.region),
    };
    // CP の validateProfile と同じ最低条件。ここで落としておかないと、
    // 400 が並んで「何件入ったのか」が利用者に見えなくなる。
    if (!p.label || !/^https:\/\/\S+$/.test(p.startUrl) || !p.ssoRegion) {
      plan.skippedProfiles.push({ label: p.label, reason: "invalid" });
      continue;
    }
    if (willHave.has(key(p.label))) {
      plan.skippedProfiles.push({ label: p.label, reason: "exists" });
      continue;
    }
    willHave.add(key(p.label));
    plan.profiles.push(p);
  }
  const seenHost = new Set(haveHost);
  for (const raw of section.hosts || []) {
    const h: SsmHostEntry = {
      alias: str(raw?.alias),
      profile: str(raw?.profile),
      instanceId: str(raw?.instanceId),
      documentName: str(raw?.documentName),
      region: str(raw?.region),
    };
    if (!h.alias || !h.instanceId) {
      plan.skippedHosts.push({ alias: h.alias, reason: "invalid" });
      continue;
    }
    const id = key(h.alias) + "\u0000" + key(h.instanceId);
    if (seenHost.has(id)) {
      plan.skippedHosts.push({ alias: h.alias, reason: "exists" });
      continue;
    }
    if (!h.profile || !willHave.has(key(h.profile))) {
      plan.skippedHosts.push({ alias: h.alias, reason: "no_profile" });
      continue;
    }
    seenHost.add(id);
    plan.hosts.push(h);
  }
  return plan;
}

/** 表示名 → CP の id 表。作成済みのプロファイル一覧から引く。 */
export function profileIdByLabel(profiles: any[]): Map<string, string> {
  const m = new Map<string, string>();
  for (const p of profiles || []) {
    const label = key(str(p?.label));
    if (label && !m.has(label)) m.set(label, String(p?.id ?? ""));
  }
  return m;
}

// --- 概要 -----------------------------------------------------------------------

export interface BundleSummary {
  prefs: number;
  profiles: number;
  hosts: number;
  instructionBytes: number;
  instructions: boolean;
}

export function summarizeBundle(b: SettingsBundle): BundleSummary {
  const s = b.sections;
  return {
    prefs: s.prefs ? Object.keys(s.prefs).length : 0,
    profiles: s.ssm?.profiles.length ?? 0,
    hosts: s.ssm?.hosts.length ?? 0,
    instructionBytes: s.instructions ? utf8Bytes(s.instructions.text) : 0,
    instructions: !!s.instructions,
  };
}

export function utf8Bytes(s: string): number {
  if (typeof TextEncoder !== "undefined") return new TextEncoder().encode(s).byteLength;
  return s.length;
}
