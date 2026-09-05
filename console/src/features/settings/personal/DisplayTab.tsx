import type { ReactNode } from "react";
import {
  useSettings,
  setSetting,
  CODE_FONTS,
  CHAT_FONTS,
  CJK_FONTS,
  CJK_FONT_AUTO,
  CJK_FONT_OFF,
  READER_FONTS,
  fontStack,
  termFontStack,
  chatFontStack,
  readerFontStack,
  ICON_SETS,
  THEMES,
  REGION_THEMES,
  SURFACE_TARGETS,
  LOCALES,
  PANE_LAYOUTS,
} from "../../../lib/settings.ts";
import { FONT_MIN, FONT_MAX } from "../../../lib/viewFont.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { MsgKey } from "../../../lib/i18n/index.ts";
import FileIcon from "../../../ui/FileIcon.tsx";
import { SwatchGrid } from "../../../ui/SwatchGrid.tsx";
import { Choice, OnOff, Row } from "../parts/controls.tsx";
import type { ChoiceProps } from "../parts/controls.tsx";

// DisplayTab: font + file-viewer preferences (CodeLeaf-inspired), persisted via the
// settings store. Every control is a horizontal selection (segmented buttons /
// chips / stepper) for a consistent feel. Terminal and viewer fonts are separate.
export function DisplayTab() {
  const s = useSettings();
  const tr = useT();
  return (
    <div className="display-settings">
      <section className="ds-group">
        <Row label={tr("settings.language")}>
          <Choice
            value={s.locale}
            options={LOCALES.map((l) => [l.id, l.label])}
            onChange={(v) => setSetting("locale", v)}
          />
        </Row>
      </section>
      <section className="ds-group">
        <h4 className="ds-title">{tr("display.pane_layout")}</h4>
        <Row label={tr("display.pane_layout") }>
          <Choice
            value={s.paneLayout}
            options={PANE_LAYOUTS.map((p) => [p.id, tr(p.labelKey)])}
            onChange={(v) => setSetting("paneLayout", v as "split" | "tabs")}
          />
        </Row>
        <p className="muted ds-note">{tr("display.pane_layout_note")}</p>
      </section>
      <section className="ds-group">
        <h4 className="ds-title">{tr("display.color_theme")}</h4>
        <Row label={tr("display.theme")}>
          <Choice
            value={s.theme}
            options={THEMES.map((x) => [x.id, tr(x.labelKey)])}
            onChange={(v) => setSetting("theme", v)}
          />
        </Row>
        <Row label={tr("display.session_theme")}>
          <Choice
            value={s.mirrorTheme}
            options={REGION_THEMES.map((x) => [x.id, tr(x.labelKey)])}
            onChange={(v) => setSetting("mirrorTheme", v)}
          />
        </Row>
        {/* A shared session (docs/log/59) is a surface for reading someone else's
            conversation. Letting it take a different theme/background from your own mirror
            makes the colour say which of the two you are looking at. */}
        <Row label={tr("display.shared_theme")}>
          <Choice
            value={s.sharedTheme}
            options={REGION_THEMES.map((x) => [x.id, tr(x.labelKey)])}
            onChange={(v) => setSetting("sharedTheme", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("display.region_theme_note")}</p>
        {/* assistantColor + assistantTheme moved to the Assistant tab (its appearance
            lives with its behavior); every other surface color stays here. */}
        {SURFACE_TARGETS.filter((t) => t.key !== "assistantColor").map((t) => (
          <Row key={t.key} label={tr(t.longKey)}>
            <SwatchGrid theme={s.theme} value={s[t.key]} onChange={(v) => setSetting(t.key, v)} />
          </Row>
        ))}
      </section>

      {/* The Japanese font applies to every surface from this one place (a unicode-range
          @font-face put at the head of --mono and fontStack). It only routes the characters
          that a Latin font does have but draws half-width (①②③ and the like) to the Japanese
          side, so it sits above the per-surface font choice. The terminal is the one exception
          (see termFontStack in settings.ts). */}
      <section className="ds-group">
        <h4 className="ds-title">{tr("display.cjk_font")}</h4>
        <Row label={tr("display.font")}>
          <ChipChoice
            value={s.cjkFont}
            options={CJK_FONTS.map((f) => [f, FONT_LABEL_KEYS[f] ? tr(FONT_LABEL_KEYS[f]) : f])}
            onChange={(v) => setSetting("cjkFont", String(v))}
          />
        </Row>
        <Row label={tr("display.preview")}>
          <span className="cjk-preview">{tr("display.cjk_font_sample")}</span>
        </Row>
        <p className="muted ds-note">{tr("display.cjk_font_note")}</p>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("display.terminal")}</h4>
        <Row label={tr("display.font")}>
          <FontSelect value={s.termFont} onChange={(v) => setSetting("termFont", v)} stack={termFontStack} />
        </Row>
        <Row label={tr("display.font_size")}>
          <Stepper value={s.termSize} onChange={(v) => setSetting("termSize", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("display.file_viewer")}</h4>
        <Row label={tr("display.font")}>
          <FontSelect value={s.viewerFont} onChange={(v) => setSetting("viewerFont", v)} />
        </Row>
        <Row label={tr("display.font_size")}>
          <Stepper value={s.viewerSize} onChange={(v) => setSetting("viewerSize", v)} />
        </Row>
        <Row label={tr("display.tab_width")}>
          <Choice value={s.tabSize} options={[[2, "2"], [4, "4"], [8, "8"]]} onChange={(v) => setSetting("tabSize", v)} />
        </Row>
        <Row label={tr("display.line_numbers")}>
          <OnOff value={s.lineNumbers} onChange={(v) => setSetting("lineNumbers", v)} />
        </Row>
        <Row label={tr("display.wrap")}>
          <OnOff value={s.wrap} onChange={(v) => setSetting("wrap", v)} />
        </Row>
        <Row label={tr("display.minimap")}>
          <OnOff value={s.minimap} onChange={(v) => setSetting("minimap", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("display.markdown")}</h4>
        <Row label={tr("display.markdown_wrap")}>
          <OnOff value={s.markdownCodeWrap} onChange={(v) => setSetting("markdownCodeWrap", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("display.session_mirror")}</h4>
        <Row label={tr("display.font")}>
          <FontSelect value={s.chatFont} onChange={(v) => setSetting("chatFont", v)} fonts={CHAT_FONTS} stack={chatFontStack} />
        </Row>
        <Row label={tr("display.font_size")}>
          <Stepper value={s.chatSize} onChange={(v) => setSetting("chatSize", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("display.reader_view")}</h4>
        <Row label={tr("display.font")}>
          <FontSelect value={s.readerFont} onChange={(v) => setSetting("readerFont", v)} fonts={READER_FONTS} stack={readerFontStack} />
        </Row>
        <Row label={tr("display.font_size")}>
          <Stepper value={s.readerSize} onChange={(v) => setSetting("readerSize", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">{tr("display.file_icons")}</h4>
        <Row label={tr("display.icon_set")}>
          <ChipChoice
            value={s.iconSet}
            options={ICON_SETS.map((x) => [x.id, tr(x.labelKey)])}
            onChange={(v) => setSetting("iconSet", v)}
          />
        </Row>
        <Row label={tr("display.preview")}>
          <span className="icon-preview">
            {["main.py", "lib.rs", "App.tsx", "style.css", "Dockerfile", "main.go", "data.json", "README.md"].map((n) => (
              <span key={n} className="icon-preview-item" title={n}>
                <FileIcon name={n} /> {n}
              </span>
            ))}
          </span>
        </Row>
      </section>

      <p className="muted ds-note">
        {tr("display.preview")}: <span style={{ fontFamily: fontStack(s.viewerFont) }}>const x = (a) =&gt; a !== 0;</span>
      </p>
    </div>
  );
}

// Generic font names carry a translated display label; brand names (Source Code Pro …)
// pass through. The stored value stays the raw name so fontStack() keeps matching.
// i18n-exempt-start: keys are the raw font values fontStack matches on (the display label comes from font.*, docs/log/28 §2.4)
const FONT_LABEL_KEYS: Record<string, MsgKey> = {
  "システム等幅": "font.sys_mono",
  "システム": "font.sys",
  "セリフ": "font.serif",
  "明朝": "font.mincho",
  "ゴシック": "font.gothic",
  [CJK_FONT_AUTO]: "font.cjk_auto",
  [CJK_FONT_OFF]: "font.cjk_off",
};
// i18n-exempt-end

// FontSelect lays the choices out horizontally, each rendered in its own font so
// the user can compare them at a glance.
function FontSelect({
  value,
  onChange,
  fonts = CODE_FONTS,
  stack = fontStack,
}: {
  value: string;
  onChange: (v: string) => void;
  fonts?: string[];
  stack?: (f: string) => string;
}) {
  const tr = useT();
  return (
    <div className="font-choices">
      {fonts.map((f) => (
        <button
          key={f}
          type="button"
          className={"font-chip" + (f === value ? " active" : "")}
          style={{ fontFamily: stack(f) }}
          onClick={() => onChange(f)}
        >
          {FONT_LABEL_KEYS[f] ? tr(FONT_LABEL_KEYS[f]) : f}
        </button>
      ))}
    </div>
  );
}

// ChipChoice lays the options out as wrapping chips (same as FontSelect) so a long
// list doesn't overflow off-screen on a phone, unlike the single-row segmented Choice.
function ChipChoice({ value, options, onChange }: ChoiceProps) {
  return (
    <div className="font-choices">
      {options.map(([v, label]) => (
        <button
          key={String(v)}
          type="button"
          className={"font-chip" + (v === value ? " active" : "")}
          onClick={() => onChange(v)}
        >
          {label as ReactNode}
        </button>
      ))}
    </div>
  );
}

// Stepper keeps font size button-driven but allows any size in range.
// The range is shared with lib/viewFont.ts, so the Alt+= / Alt+- shortcuts stop at the same
// bounds.
function Stepper({
  value,
  onChange,
  min = FONT_MIN,
  max = FONT_MAX,
}: {
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
}) {
  const tr = useT();
  const set = (n: number) => onChange(Math.min(max, Math.max(min, n)));
  return (
    <div className="stepper">
      <button type="button" onClick={() => set(value - 1)} disabled={value <= min} aria-label={tr("display.smaller")}>
        −
      </button>
      <span className="stepper-val">{value}px</span>
      <button type="button" onClick={() => set(value + 1)} disabled={value >= max} aria-label={tr("display.larger")}>
        ＋
      </button>
    </div>
  );
}
