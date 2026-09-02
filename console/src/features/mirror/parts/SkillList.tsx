import type { Ref } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import type { SessionSkill } from "../../../core/api/client.ts";
import { SkillOriginBadge } from "./SkillOriginBadge.tsx";

/**
 * スキルピッカー（docs/log/50）: コンポーサー上に浮く補完リスト。マウスは onMouseMove で
 * 選択追従＋クリック確定（mousedown は preventDefault でフォーカスを奪わない —
 * CommandPalette と同型）、タップはそのまま確定、キーボードは呼び出し側の onKeyDown が駆動。
 * 引数入力中（`passive`）は受動表示 — キーボード選択を持たないので sel も付けず、
 * クリックだけ（引数は残したままコマンドを差し替える）が生きる。
 */
export function SkillList({
  popRef,
  selRef,
  passive,
  /** null = まだ取得していない（スピナー）。 */
  skills,
  items,
  sel,
  query,
  onHover,
  onPick,
}: {
  popRef: Ref<HTMLDivElement>;
  selRef: Ref<HTMLButtonElement>;
  passive: boolean;
  skills: SessionSkill[] | null;
  items: SessionSkill[];
  sel: number;
  query: string;
  onHover: (i: number) => void;
  onPick: (s: SessionSkill) => void;
}) {
  return (
    <div className={"mirror-skills" + (passive ? " passive" : "")} ref={popRef} role="listbox" aria-label={tr("mirror.skills_btn")}>
      {skills === null ? (
        <div className="mirror-skills-note">
          <Icon name="loading" spin /> {tr("mirror.skills_loading")}
        </div>
      ) : items.length === 0 ? (
        // 絞り込みの結果ゼロ（ボタン起点だけがここへ来る — タイプ起点は非表示にする）と
        // そもそも 1 つも無いのは別の話なので、文言を分ける。
        <div className="mirror-skills-note">{tr(query ? "mirror.skills_no_match" : "mirror.skills_empty")}</div>
      ) : (
        items.map((s, i) => (
          <button
            type="button"
            key={s.type + ":" + s.source + ":" + s.name}
            ref={!passive && i === sel ? selRef : undefined}
            className={"mirror-skill-item" + (!passive && i === sel ? " sel" : "")}
            role="option"
            aria-selected={!passive && i === sel}
            title={tr("mirror.skills_item_hint")}
            onMouseMove={() => onHover(i)}
            onMouseDown={(ev) => ev.preventDefault()}
            onClick={() => onPick(s)}
          >
            {/* 1 行目＝起動文字列＋引数ヒント＋出所バッジ、2 行目＝説明。説明を
                独立行にすることで、名前と引数に幅を食われず全幅で読める。 */}
            <span className="mirror-skill-head">
              <span className="mirror-skill-name">{s.invoke ? s.invoke.trim() : s.name}</span>
              {s.argumentHint ? <span className="mirror-skill-hint">{s.argumentHint}</span> : null}
              {/* バッジは 1 つの入れ物に — 直接並べて margin-left:auto を各々に付けると、
                  2 つ出たとき余白が両者に均等配分されて右端に寄らない。 */}
              <span className="mirror-skill-badges">
                {s.origin ? <SkillOriginBadge origin={s.origin} /> : null}
                {s.source === "user" ? <span className="mirror-skill-src">{tr("mirror.skills_src_user")}</span> : null}
                {s.source === "cli" ? <span className="mirror-skill-src">{tr("mirror.skills_src_cli")}</span> : null}
              </span>
            </span>
            {s.description ? <span className="mirror-skill-desc">{s.description}</span> : null}
          </button>
        ))
      )}
    </div>
  );
}

/** 「/」ボタン: マウス/タップだけでスキルを呼ぶ入口（キーボード派は素の「/」タイプ）。 */
export function SkillButton({
  btnRef,
  open,
  disabled,
  trigger,
  onToggle,
}: {
  btnRef: Ref<HTMLButtonElement>;
  open: boolean;
  disabled: boolean;
  /** "" のときはグリフを持たない kind（ボタンのみで開く）— 汎用の ✦ を出す。 */
  trigger: string;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      ref={btnRef}
      className={"ghost mirror-skill-btn" + (open ? " on" : "")}
      title={tr("mirror.skills_btn")}
      disabled={disabled}
      onClick={onToggle}
    >
      <span className="mirror-skill-glyph" aria-hidden="true">
        {trigger || "✦"}
      </span>
    </button>
  );
}
