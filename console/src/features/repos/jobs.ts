// リポジトリ取り込みジョブ（docs/log/78）。`git clone` / `svn checkout` は Agent 側で
// バックグラウンドのジョブになり、Console は GET /api/repo-jobs でその**実際の**進行を
// 見る。以前は POST の応答を待つだけで、応答が返らなかった（= 上流のプロキシが 60 秒で
// 諦めた）ときに「フォルダができているから成功」と読み替えていたため、走行中の作業コピーを
// 取り込み済みとして並べていた。
//
// この層が持つ性質:
//   - **タブを閉じても消えない**。進行はサーバにあるので、再読み込みしても別タブでも同じ行が出る。
//   - **終端は既読にするまで残る**（失敗・中断）。結末を見る前に消えると「黙って失敗した」に戻る。
//   - 走行中だけポーリングを速める（2 秒）。何も走っていなければリポジトリ一覧と同じ 60 秒。
import { create } from "zustand";
import { api, isTransientErr, raw } from "../../core/api/client.ts";
import { useWorkspaceStore, wsRunning } from "../../core/store/workspace.ts";
import { useReposStore } from "./store.ts";

export type RepoJobState = "running" | "done" | "failed" | "canceled" | "interrupted";

export interface RepoJob {
  id: string;
  kind: "git" | "svn";
  name: string;
  path?: string;
  url?: string;
  state: RepoJobState;
  /** 最後に見えた出力行（svn の "A path" / git の "Receiving objects: …"）。 */
  progress?: string;
  /** 取得した行数。総数は svn も git も事前に教えてくれないので、割合にはできない。 */
  items?: number;
  error?: string;
  /** 失敗したが作業コピーは残っている（svn なら 更新 で続きから取れる）。 */
  kept?: boolean;
  startedAt: string;
  endedAt?: string;
}

export const isRepoJobRunning = (j: RepoJob) => j.state === "running";

const FAST_POLL_MS = 2000;
const IDLE_POLL_MS = 60000;

interface RepoJobsStore {
  jobs: RepoJob[];
  /** 一覧を取り直す。過渡的失敗（起動直後の 502）では前の内容を残す。 */
  refresh(): Promise<RepoJob[]>;
  /** 走行中なら中止、終端済みなら既読（どちらも DELETE /api/repo-jobs/{id}）。 */
  remove(id: string): Promise<void>;
  /** id が走行中でなくなるまで待つ。消えていた場合は null。 */
  wait(id: string): Promise<RepoJob | null>;
}

// 同時に飛んだ refresh を 1 本にまとめる。取り込み中は表示のポーラーと wait() の両方が
// 見に来るので、まとめないと同じ GET が二重に出る。
let inflight: Promise<RepoJob[]> | null = null;

export const useRepoJobsStore = create<RepoJobsStore>((set, get) => ({
  jobs: [],
  refresh() {
    if (inflight) return inflight;
    inflight = (async () => {
      let d: { jobs?: RepoJob[] };
      try {
        d = await api("api/repo-jobs");
      } catch {
        return get().jobs; // network drop — keep what we have
      } finally {
        inflight = null;
      }
      if (isTransientErr(d)) return get().jobs;
      const jobs = Array.isArray(d.jobs) ? d.jobs : [];
      const before = get().jobs;
      set({ jobs });
      // 終端に落ちた瞬間、そのフォルダは「取り込み中」から本物の作業コピーになる
      // （Agent は走行中のものを GET /repos に出さない）。一覧を取り直して行を出す。
      const settled = before.some((b) => isRepoJobRunning(b) && !jobs.some((j) => j.id === b.id && isRepoJobRunning(j)));
      if (settled) void useReposStore.getState().refresh();
      return jobs;
    })();
    return inflight;
  },
  async remove(id) {
    await raw(`api/repo-jobs/${encodeURIComponent(id)}`, { method: "DELETE" });
    set({ jobs: get().jobs.filter((j) => j.id !== id) });
    await get().refresh();
  },
  async wait(id) {
    for (;;) {
      const jobs = await get().refresh();
      const j = jobs.find((x) => x.id === id);
      if (!j) return null; // 別タブで既読にされた / Agent が忘れた
      if (!isRepoJobRunning(j)) return j;
      await new Promise((r) => setTimeout(r, FAST_POLL_MS));
    }
  },
}));

/** 走行中は 2 秒、そうでなければ 60 秒でポーリングする。返り値は停止関数（StrictMode 対応）。 */
export function startRepoJobsPolling(): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const tick = async () => {
    if (!document.hidden && wsRunning(useWorkspaceStore.getState().state)) {
      await useRepoJobsStore.getState().refresh();
    }
    if (stopped) return;
    const fast = useRepoJobsStore.getState().jobs.some(isRepoJobRunning);
    timer = setTimeout(() => void tick(), fast ? FAST_POLL_MS : IDLE_POLL_MS);
  };
  void tick();
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
  };
}
