export interface ProjectRef {
  slug: string;
  display: string;
}
export interface MemoryRoot {
  kind: string;
  label: string;
  scopes: boolean;
  files: number;
  bytes: number;
  modified?: string;
  busy?: boolean;
  projects: ProjectRef[];
  /** エージェント側がメモリを書くことの ON/OFF（今は codex のみ・docs/log/39 P4）。 */
  toggleable?: boolean;
  enabled?: boolean;
}
/**
 * 宣言はされているが今この環境では有効でないルート（docs/log/39 P4）。codex の memories は
 * 上流の既定が OFF なので、黙って一覧から消すと「なぜ出てこないか」も「どう有効化するか」も
 * 伝わらない。toggleable なものはここから直接切り替える。
 */
export interface InactiveRoot {
  kind: string;
  label: string;
  reason: string;
  toggleable?: boolean;
  enabled?: boolean;
}
export interface RootsPayload {
  roots: MemoryRoot[];
  inactive?: InactiveRoot[];
  auto: boolean;
  autoLocked: boolean;
  lastSnapshot?: string;
}
export interface Snapshot {
  rev: string;
  short: string;
  at: string;
  subject: string;
  trigger: string;
  kinds: string[];
  projects: ProjectRef[];
  files: number;
}
export interface TreeKind {
  kind: string;
  label: string;
  scopes: boolean;
  files: number;
  bytes: number;
}
export interface TreeProject extends ProjectRef {
  files: number;
  bytes: number;
}
/** 秘密情報らしき記述の検出結果。値そのものは Agent 側でマスク済み（hint のみ）。 */
export interface SecretFinding {
  path: string;
  line: number;
  rule: string;
  hint: string;
  history?: boolean;
}
/** 取り込んだ系譜の概況（POST import の応答）。適用範囲はここから選ぶ。 */
export interface ImportPreview {
  importId: string;
  format: string;
  head: string;
  headTs?: string;
  snapshots: number;
  kinds: TreeKind[];
  projects: TreeProject[];
  unavailable: string[];
  rejected: string[];
  secrets: SecretFinding[];
  /** 秘密のスキャン自体が失敗したことを示す旗（Go 側 memoryImportPreview.SecretScanFailed）。
   *  🔴 `secrets: []` と意味が違う——「検出なし」ではなく「検出できなかった」。
   *  Go 側は `json:"secretScanFailed,omitempty"` なので **false のときキーが出ない**。
   *  ここで optional にして「未定義＝false」で扱うのは意図的で、この面では
   *  「キーが無い」と「false」が同じ意味になるのが正しい（走査は成功したが旗が立たなかった
   *  ＝失敗していない）。**真のときだけ意味を持つ旗**なので、ゼロ値と欠落を潰してよい。 */
  secretScanFailed?: boolean;
}
