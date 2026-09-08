import type { Ref } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr, tMaybe } from "../../../lib/i18n/index.ts";
import type { SessionSkill } from "../../../core/api/client.ts";
import { SkillOriginBadge } from "./SkillOriginBadge.tsx";

/**
 * Skill picker (docs/log/50): the completion list floating over the composer. With a mouse,
 * onMouseMove moves the selection and a click commits (mousedown is preventDefault-ed so focus
 * stays in the input, as in CommandPalette); a tap commits directly; the keyboard is driven by
 * the caller's onKeyDown. While arguments are being typed (`passive`) the list is display-only:
 * there is no keyboard selection, so no sel is applied and only clicking works, which swaps the
 * command and leaves the arguments in place. `more` > 0 appends the "show N more" row that
 * unfolds the CLI-bundled tier (docs/log/50 §9); it takes the selection index right after
 * the last item.
 */
export function SkillList({
  popRef,
  selRef,
  passive,
  /** null means not fetched yet, which renders the spinner. */
  skills,
  items,
  more,
  trigger,
  sel,
  query,
  onHover,
  onPick,
  onMore,
}: {
  popRef: Ref<HTMLDivElement>;
  selRef: Ref<HTMLButtonElement>;
  passive: boolean;
  skills: SessionSkill[] | null;
  items: SessionSkill[];
  more: number;
  /** The kind's trigger glyph ("" = none); the "show more" row names the "//" shortcut with it. */
  trigger: string;
  sel: number;
  query: string;
  onHover: (i: number) => void;
  onPick: (s: SessionSkill) => void;
  onMore: () => void;
}) {
  const moreSel = !passive && more > 0 && sel === items.length;
  return (
    <div className={"mirror-skills" + (passive ? " passive" : "")} ref={popRef} role="listbox" aria-label={tr("mirror.skills_btn")}>
      {skills === null ? (
        <div className="mirror-skills-note">
          <Icon name="loading" spin /> {tr("mirror.skills_loading")}
        </div>
      ) : items.length === 0 && more === 0 ? (
        // "Filtered down to nothing" (only reachable when opened from the button; typing hides
        // the list instead) and "there are none at all" are different situations, so the wording
        // differs too.
        <div className="mirror-skills-note">{tr(query ? "mirror.skills_no_match" : "mirror.skills_empty")}</div>
      ) : (
        <>
        {items.map((s, i) => (
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
            {/* Line 1 is the invoke string, the argument hint and the origin badges; line 2 is
                the description. Giving the description its own line lets it use the full width
                instead of competing with the name and arguments. */}
            <span className="mirror-skill-head">
              <span className="mirror-skill-name">{s.invoke ? s.invoke.trim() : s.name}</span>
              {s.argumentHint ? <span className="mirror-skill-hint">{s.argumentHint}</span> : null}
              {/* The badges need one container: laid out directly with margin-left:auto on each,
                  two of them split the free space evenly and neither reaches the right edge. */}
              <span className="mirror-skill-badges">
                {s.origin ? <SkillOriginBadge origin={s.origin} /> : null}
                {s.source === "user" ? <span className="mirror-skill-src">{tr("mirror.skills_src_user")}</span> : null}
                {s.source === "cli" ? <span className="mirror-skill-src">{tr("mirror.skills_src_cli")}</span> : null}
              </span>
            </span>
            {describe(s) ? <span className="mirror-skill-desc">{describe(s)}</span> : null}
          </button>
        ))}
        {more > 0 ? (
          <button
            type="button"
            ref={moreSel ? selRef : undefined}
            className={"mirror-skill-more" + (moreSel ? " sel" : "")}
            role="option"
            aria-selected={moreSel}
            onMouseMove={() => onHover(items.length)}
            onMouseDown={(ev) => ev.preventDefault()}
            onClick={onMore}
          >
            <span>{tr("mirror.skills_more_cli", { n: more })}</span>
            {trigger ? <kbd className="mirror-skill-more-key">{trigger + trigger}</kbd> : null}
          </button>
        ) : null}
        </>
      )}
    </div>
  );
}

// describe: the description line. A CLI-bundled entry arrives with its name only (claude's
// init frame carries no descriptions - docs/log/50 §9), so the well-known ones are described
// from a small i18n table; anything else shows its name alone.
function describe(s: SessionSkill): string | undefined {
  if (s.description) return s.description;
  if (s.source === "cli") return tMaybe("mirror.skills_cli_desc." + s.name);
  return undefined;
}

/** The slash button: the mouse/tap entry point for skills (keyboard users just type "/"). */
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
  /** "" means a kind with no trigger glyph (opened by button only); a generic glyph is shown. */
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
