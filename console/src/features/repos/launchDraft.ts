// 「作業を始める」の最初のプロンプトの下書き — リポジトリ毎に localStorage へ持つ。
// モーダルを閉じただけでは消さない（場所やブランチを見に行って戻る、別の画面で確認して
// から書き足す、が普通の使い方で、そのたびに打った文章が消えるのは事故に近い）。消える
// のは**セッションが実際に起動できたとき**だけで、そこは prompt が新しいセッションへ渡り
// 切った唯一の地点でもある（履歴には pushPromptHistory が別に残す）。
//
// 端末ローカルなのは launchPrefs と同じ理由で、書きかけの文章は「この端末で開いていた箱の
// 中身」であって、他の端末の起動ダイアログに勝手に現れてよいものではないため。
import { useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import { clearDraft, readDraft } from "../../lib/draft.ts";

const KEY = (repo: string): string | null => (repo ? "af.launch-prompt." + repo : null);

// 添付（貼り付けた画像）の下書きの鍵。文章と同じくリポジトリ毎・同じ寿命だが、置き場は
// IndexedDB（lib/attachDraft）— localStorage に画像のバイト列を置くと、設定や UI の
// 覚え書きが同居している 5MB の枠をこれだけで使い切る。
export const launchAttachKey = (repo: string): string | null => (repo ? "af.launch-attach." + repo : null);

export function readLaunchPrompt(repo: string): string {
  return readDraft(KEY(repo));
}

export function clearLaunchPrompt(repo: string): void {
  clearDraft(KEY(repo));
}

// useLaunchPrompt は useState<string> を localStorage で裏打ちしたもの（lib/draft の
// useDraft と同型）。違いは 2 つ:
//   - seed（引き継ぎ提案・メモ送信・作業項目が流し込む初期プロンプト）は下書きより強い。
//     呼び出し側が「この文章で始める」と決めて開いた箱なので、前の書きかけで上書きしない。
//   - 返り値の clear() は保存済みの下書きを消したうえで、以後の書き戻しを止める。起動成功
//     から unmount までの間に再描画が挟まっても、消したはずの下書きが蘇らないようにする。
export function useLaunchPrompt(
  repo: string,
  seed?: string,
): [string, Dispatch<SetStateAction<string>>, () => void] {
  const key = KEY(repo);
  const [prompt, setPrompt] = useState(() => seed ?? readDraft(key));
  const keyRef = useRef(key);
  const launchedRef = useRef(false);
  useEffect(() => {
    if (keyRef.current !== key) {
      // 開いたままリポジトリが変わった（はじめる ハブから別のコピーを選び直した）。
      // 前のリポジトリの文章を新しい鍵へ書き込まず、そちらの下書きを読み直す。
      keyRef.current = key;
      launchedRef.current = false;
      setPrompt(readDraft(key));
      return;
    }
    if (launchedRef.current || !key) return;
    try {
      if (prompt) localStorage.setItem(key, prompt);
      else localStorage.removeItem(key);
    } catch {
      /* private mode / quota — 下書きが残らないだけ */
    }
  }, [prompt, key]);
  const clear = () => {
    launchedRef.current = true;
    clearDraft(key);
  };
  return [prompt, setPrompt, clear];
}
