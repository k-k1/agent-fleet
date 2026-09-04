// 表示位置の記憶をスクロール容器に取り付ける ref（記憶そのものは ../scrollMemory.ts）。
//
// 取り付けた容器について、
//   - 読み手がスクロールするたび位置を控え、
//   - 付いた瞬間（＝タブから戻ってきた再マウント）と、面が hidden から戻った瞬間に
//     控えた位置へ戻す。
//
// 戻すのが 1 回で済まないのがこの面の厄介なところで、**高さは遅れて確定する**:
// Markdown プレビューは MarkdownView が innerHTML を passive effect で書き、そのあと
// ハイライト → 画像 → フォントで伸びる。PDF は全ページの寸法を取ってから箱が伸びる。
// 付いた瞬間は scrollHeight === clientHeight（＝戻す先が無い）ことが普通なので、
// 中身が伸びるのを見張って届くまで押し続ける。打ち切りは
//   1. 目的地に届いた（1 フレーム置いて押さえ直してから終う）、
//   2. 読み手が触った（wheel / touch / pointer / key）、
//   3. 猶予切れ（中身が縮んだ等で二度と届かないとき）
// の 3 つだけ。**scrollTop が自分の書いた値とズレたことを「触った」と読んではいけない**
// —— ブラウザのスクロールアンカリングが遅延レイアウトのたびに動かすので、ミラーでは
// それで目的地の手前に固着した（[[mirror-scroll-position]]）。
import { useMemo } from "react";
import { loadScrollPos, saveScrollPos } from "../scrollMemory.ts";

/** 中身の伸びを待つ猶予。これを過ぎたら「もう届かない」とみなして今の位置を控える。
 *  ハイライトも画像も終わったあとの Markdown で実測 1s 未満。 */
const RESTORE_BUDGET_MS = 4000;

/** 復元を打ち切る「読み手が触った」の実体。scroll は入れない（自分の書き戻しと
 *  ブラウザのアンカリングが同じ形で届くので区別できない）。 */
const USER_EVENTS = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

/** React 19 の ref コールバック（戻り値が取り外しの後始末）。 */
export type ScrollMemoryRef = (el: HTMLElement | null) => (() => void) | void;

/** 面（"code" / "preview" / "pdf" …）ごとの ref を返す。同じ面には**同じ関数**を
 *  返すこと ——毎レンダー別の関数を渡すと React が「外して付け直す」ので、後始末が
 *  毎レンダー走る（[[react-ref-callback-identity-cleanup]]）。 */
export type ScrollMemoryFactory = (surface: string) => ScrollMemoryRef;

const noop: ScrollMemoryRef = () => {};

export function scrollMemoryRef(key: string | null): ScrollMemoryRef {
  if (!key) return noop;
  return (el) => {
    if (!el) return;
    let target = loadScrollPos(key);
    let restoring = false;
    let timer = 0;
    let settle = 0;
    // 面が hidden（編集タブ）やペインの畳み込みで箱を失うと scrollTop は 0 に落ちる。
    // 「箱が戻ってきた」を復元のきっかけにするため、見えているかを持っておく。
    let boxed = el.clientHeight > 0;

    const remember = () => {
      // 箱を失っている間の 0 は「先頭まで戻した」ではないので控えない。
      if (el.clientHeight > 0) saveScrollPos(key, el.scrollTop);
    };

    const finish = () => {
      if (!restoring) return;
      restoring = false;
      window.clearTimeout(timer);
      if (settle) cancelAnimationFrame(settle);
      settle = 0;
      contentRo.disconnect();
      mo.disconnect();
      remember();
    };

    // 目的地へ寄せる。まだ中身が足りなければ何もしない（次の伸びで呼ばれる）。
    const apply = () => {
      if (!restoring || target == null) return;
      const max = el.scrollHeight - el.clientHeight;
      if (max <= 0) return;
      el.scrollTop = Math.min(target, max);
      if (Math.abs(el.scrollTop - target) > 1 || settle) return;
      // 届いた。ただし**この直後に走るレイアウト effect**（CodeView の引用行への
      // scrollIntoView）が上書きしうるので、1 フレーム置いて押さえ直してから終う。
      settle = requestAnimationFrame(() => {
        settle = 0;
        const room = el.scrollHeight - el.clientHeight;
        if (room > 0 && target != null) el.scrollTop = Math.min(target, room);
        finish();
      });
    };

    const contentRo = new ResizeObserver(() => apply());
    // 中身の伸びは容器の RO では見えない（scrollHeight は box size ではない）ので
    // 子を見る。innerHTML の書き換えで子ごと入れ替わる面があるため、子の増減も見る。
    const mo = new MutationObserver(() => {
      watchContent();
      apply();
    });

    const watchContent = () => {
      contentRo.disconnect();
      contentRo.observe(el);
      for (const child of Array.from(el.children)) contentRo.observe(child);
    };

    const arm = () => {
      target = loadScrollPos(key);
      if (target == null || target <= 0) return;
      if (Math.abs(el.scrollTop - target) <= 1) return; // すでにその位置
      restoring = true;
      watchContent();
      mo.observe(el, { childList: true });
      window.clearTimeout(timer);
      timer = window.setTimeout(finish, RESTORE_BUDGET_MS);
      apply();
    };

    // 箱の有無だけを見る RO は付けっぱなし: hidden から戻った面（編集⇄表示）は
    // 再マウントされないので、これが唯一のきっかけになる。
    const boxRo = new ResizeObserver(() => {
      const now = el.clientHeight > 0;
      if (now && !boxed) arm();
      boxed = now;
    });

    const onScroll = () => {
      if (!restoring) remember();
    };
    const onUser = () => finish();

    el.addEventListener("scroll", onScroll, { passive: true });
    for (const type of USER_EVENTS) el.addEventListener(type, onUser, { passive: true });
    boxRo.observe(el);
    arm();

    return () => {
      // 復元の途中経過は書き戻さない（控えてある目的地を潰してしまう）。
      if (!restoring) remember();
      restoring = false;
      window.clearTimeout(timer);
      if (settle) cancelAnimationFrame(settle);
      contentRo.disconnect();
      boxRo.disconnect();
      mo.disconnect();
      el.removeEventListener("scroll", onScroll);
      for (const type of USER_EVENTS) el.removeEventListener(type, onUser);
    };
  };
}

/** 1 つのファイル（＝1 つのキーの前半）について、面ごとの ref を配る。キーが
 *  変われば配り直す＝別のファイルの位置を引き継がない。 */
export function useScrollMemory(baseKey: string | null): ScrollMemoryFactory {
  return useMemo(() => {
    const refs = new Map<string, ScrollMemoryRef>();
    return (surface: string) => {
      let ref = refs.get(surface);
      if (!ref) {
        ref = scrollMemoryRef(baseKey ? `${baseKey}\u0000${surface}` : null);
        refs.set(surface, ref);
      }
      return ref;
    };
  }, [baseKey]);
}
