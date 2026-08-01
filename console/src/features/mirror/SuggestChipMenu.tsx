// 返信サジェストのチップに付くコンテキストメニュー（右クリック / 長タップ / Menu キー）。
// MirrorView と ChatView が同じ挙動で使う（チップ行の見た目だけ各 CSS が持つ）。
//
// 以前はチップ内の × が唯一の削除導線だった。× は @media (hover: hover) 限定＝タッチでは
// 出せず（誤タップで候補が消える）、常時 20px の余白を取るためチップも太っていた。削除は
// 「たまにやる操作」なので、ポインタ/タッチどちらからも同じメニューに集約する:
//   - 右クリック（contextmenu）… マウス
//   - 長タップ（500ms）… タッチ。Android は同じ押下で native contextmenu も飛ばすが、
//     どちらが先でも開くのは同じメニューなので実害はない（開いた後は timer を止める）。
//   - Menu キー / Shift+F10 … キーボード（rail の行と同じ contextMenuKey.ts の作法）
// 長タップで開いたときは、指を離したときの click（＝コンポーサーへ差し込み）を1回だけ捨てる。
import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, MouseEvent as RMouseEvent, TouchEvent as RTouchEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useT } from "../../lib/i18n/index.ts";
import { isContextMenuKey, menuAnchor } from "../project/contextMenuKey.ts";

// 長タップと判定するまでの時間。ブラウザ既定の長押し（選択・callout）と同じ 500ms。
const LONG_PRESS_MS = 500;
// この距離を超えて動いたら長タップではなくチップ行の横スクロール（スワイプ）。
const MOVE_TOL = 10;

export type ChipMenuState = { text: string; llm: boolean; x: number; y: number };

export type ChipMenuHandlers = {
  onContextMenu: (e: RMouseEvent) => void;
  onMouseDown: () => void;
  onTouchStart: (e: RTouchEvent) => void;
  onTouchMove: (e: RTouchEvent) => void;
  onTouchEnd: (e: RTouchEvent) => void;
  onTouchCancel: () => void;
};

export type ChipMenu = {
  /** 開いているメニュー（null = 閉じている）。<SuggestChipMenu menu={…}> へ渡す。 */
  menu: ChipMenuState | null;
  close: () => void;
  /** チップ <button> に展開するイベントハンドラ一式。 */
  chipProps: (text: string, llm: boolean) => ChipMenuHandlers;
  /** Menu キー / Shift+F10 をチップの onKeyDown で処理する。処理したら true。 */
  onKeyDown: (e: RKeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => boolean;
  /** 直前の長タップで開いた分の click か（true なら差し込みを行わない・フラグは消費される）。 */
  clickSwallowed: () => boolean;
};

export function useChipMenu(): ChipMenu {
  const [menu, setMenu] = useState<ChipMenuState | null>(null);
  const timer = useRef<number | null>(null);
  const origin = useRef<{ x: number; y: number } | null>(null);
  const swallow = useRef(false);

  const cancelTimer = () => {
    if (timer.current !== null) {
      window.clearTimeout(timer.current);
      timer.current = null;
    }
    origin.current = null;
  };

  const open = (text: string, llm: boolean, x: number, y: number) => {
    cancelTimer();
    setMenu({ text, llm, x, y });
  };

  const chipProps = (text: string, llm: boolean): ChipMenuHandlers => ({
    onContextMenu: (e) => {
      e.preventDefault(); // ブラウザ既定のメニュー / iOS の callout を出さない
      // Android の長押しはこちら（native contextmenu）が先に飛ぶことがある。指を離したときの
      // click を捨てる必要があるのはタッチ由来のときだけなので、pointerType で見分ける
      // （マウスの右クリックに click は続かない＝ここで立てると次の左クリックを食べてしまう）。
      if ((e.nativeEvent as PointerEvent).pointerType === "touch") swallow.current = true;
      open(text, llm, e.clientX, e.clientY);
    },
    // マウス操作が始まったら、右クリックで立った古い swallow を必ず落とす（右クリックの後に
    // click は来ないので、ここで消さないと次の左クリックを1回食べてしまう）。
    onMouseDown: () => {
      swallow.current = false;
    },
    onTouchStart: (e) => {
      swallow.current = false;
      cancelTimer();
      const t = e.touches[0];
      if (!t || e.touches.length > 1) return;
      origin.current = { x: t.clientX, y: t.clientY };
      const { clientX, clientY } = t;
      timer.current = window.setTimeout(() => {
        swallow.current = true; // 指を離したときの click（差し込み）を捨てる
        open(text, llm, clientX, clientY);
      }, LONG_PRESS_MS);
    },
    onTouchMove: (e) => {
      const t = e.touches[0];
      const o = origin.current;
      if (!t || !o) return;
      if (Math.abs(t.clientX - o.x) > MOVE_TOL || Math.abs(t.clientY - o.y) > MOVE_TOL) cancelTimer();
    },
    // 長タップが成立していたら touchend を preventDefault して、互換 click / mousedown の合成を
    // 止める。合成 mousedown はメニューの外側判定（useDismiss）に当たるので、これを止めないと
    // 指を離した瞬間にメニューが閉じる。効かないブラウザに備えて swallow フラグも残す
    // （次の操作の touchstart / mousedown で必ず落ちるので、居残って click を食べることはない）。
    onTouchEnd: (e) => {
      if (swallow.current && e.cancelable) e.preventDefault();
      cancelTimer();
    },
    onTouchCancel: cancelTimer,
  });

  const onKeyDown = (e: RKeyboardEvent<HTMLButtonElement>, text: string, llm: boolean): boolean => {
    if (!isContextMenuKey(e)) return false;
    e.preventDefault();
    const a = menuAnchor(e.currentTarget);
    open(text, llm, a.x, a.y);
    return true;
  };

  return {
    menu,
    close: () => {
      cancelTimer();
      setMenu(null);
    },
    chipProps,
    onKeyDown,
    clickSwallowed: () => {
      const s = swallow.current;
      swallow.current = false;
      return s;
    },
  };
}

interface SuggestChipMenuProps {
  menu: ChipMenuState;
  /** この候補がピン留め済みか（ラベルとアイコンを切り替える）。 */
  pinned: boolean;
  onClose: () => void;
  onTogglePin: (text: string) => void;
  onForget: (text: string, llm: boolean) => void;
}

/** チップのメニュー本体（カーソル位置に fixed で出す・画面外にははみ出さない）。 */
export function SuggestChipMenu({ menu, pinned, onClose, onTogglePin, onForget }: SuggestChipMenuProps) {
  const tr = useT();
  const ref = useRef<HTMLUListElement>(null);
  useDismiss([ref], true, onClose);
  useMenuRoving(ref, true);
  // 位置決めは毎レンダー・描画前に（親はポーリングで再レンダーし、その度に生の座標が
  // inline style として戻るので、一度きりのクランプでは画面外へ押し出される）。
  useLayoutEffect(() => {
    if (ref.current) placeFixed(ref.current, menu.x, menu.y);
  });
  const run = (fn: () => void) => {
    onClose();
    fn();
  };
  return createPortal(
    <ul
      className="ui-menu suggest-menu"
      ref={ref}
      style={{ left: menu.x, top: menu.y }}
      role="menu"
      onMouseDown={(e) => e.stopPropagation()}
    >
      <li>
        <button type="button" className="ui-menu-item" onClick={() => run(() => onTogglePin(menu.text))}>
          <Icon name={pinned ? "pinned" : "pin"} />
          {pinned ? tr("mirror.suggest_unpin") : tr("mirror.suggest_pin")}
        </button>
      </li>
      <li className="ui-menu-sep" />
      <li>
        <button type="button" className="ui-menu-item danger" onClick={() => run(() => onForget(menu.text, menu.llm))}>
          <Icon name="trash" />
          {tr("mirror.suggest_forget_item")}
        </button>
      </li>
    </ul>,
    document.body,
  );
}
