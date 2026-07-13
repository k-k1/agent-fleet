import type { ReactNode } from "react";
import {
  useSettings,
  setSetting,
  CODE_FONTS,
  CHAT_FONTS,
  READER_FONTS,
  fontStack,
  chatFontStack,
  readerFontStack,
  ICON_SETS,
  THEMES,
  REGION_THEMES,
  SURFACE_TARGETS,
  MIRROR_SEND_MODES,
} from "../../lib/settings.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { SwatchGrid } from "../../ui/SwatchGrid.tsx";
import { Choice, OnOff } from "./controls.tsx";
import type { ChoiceProps } from "./controls.tsx";

// DisplayTab: font + file-viewer preferences (CodeLeaf-inspired), persisted via the
// settings store. Every control is a horizontal selection (segmented buttons /
// chips / stepper) for a consistent feel. Terminal and viewer fonts are separate.
export function DisplayTab() {
  const s = useSettings();
  return (
    <div className="display-settings">
      <section className="ds-group">
        <h4 className="ds-title">カラーテーマ</h4>
        <Row label="テーマ">
          <Choice
            value={s.theme}
            options={THEMES.map((t) => [t.id, t.label])}
            onChange={(v) => setSetting("theme", v)}
          />
        </Row>
        <Row label="セッションのテーマ">
          <Choice
            value={s.mirrorTheme}
            options={REGION_THEMES.map((t) => [t.id, t.label])}
            onChange={(v) => setSetting("mirrorTheme", v)}
          />
        </Row>
        <Row label="アシスタントのテーマ">
          <Choice
            value={s.assistantTheme}
            options={REGION_THEMES.map((t) => [t.id, t.label])}
            onChange={(v) => setSetting("assistantTheme", v)}
          />
        </Row>
        <p className="muted ds-note">
          セッション（ミラー）とアシスタントチャットは、アプリ本体とは別のテーマ（ダーク／ライト）で表示できます（「アプリに合わせる」で本体に追従）。背景色も下でそれぞれ指定できます。
        </p>
        {SURFACE_TARGETS.map((t) => (
          <Row key={t.key} label={t.long}>
            <SwatchGrid theme={s.theme} value={s[t.key]} onChange={(v) => setSetting(t.key, v)} />
          </Row>
        ))}
      </section>

      <section className="ds-group">
        <h4 className="ds-title">ターミナル</h4>
        <Row label="フォント">
          <FontSelect value={s.termFont} onChange={(v) => setSetting("termFont", v)} />
        </Row>
        <Row label="文字サイズ">
          <Stepper value={s.termSize} onChange={(v) => setSetting("termSize", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">ファイルビュアー</h4>
        <Row label="フォント">
          <FontSelect value={s.viewerFont} onChange={(v) => setSetting("viewerFont", v)} />
        </Row>
        <Row label="文字サイズ">
          <Stepper value={s.viewerSize} onChange={(v) => setSetting("viewerSize", v)} />
        </Row>
        <Row label="タブ幅">
          <Choice value={s.tabSize} options={[[2, "2"], [4, "4"], [8, "8"]]} onChange={(v) => setSetting("tabSize", v)} />
        </Row>
        <Row label="行番号">
          <OnOff value={s.lineNumbers} onChange={(v) => setSetting("lineNumbers", v)} />
        </Row>
        <Row label="折り返し">
          <OnOff value={s.wrap} onChange={(v) => setSetting("wrap", v)} />
        </Row>
        <Row label="ミニマップ">
          <OnOff value={s.minimap} onChange={(v) => setSetting("minimap", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">セッション（Markdownミラー）</h4>
        <Row label="フォント">
          <FontSelect value={s.chatFont} onChange={(v) => setSetting("chatFont", v)} fonts={CHAT_FONTS} stack={chatFontStack} />
        </Row>
        <Row label="文字サイズ">
          <Stepper value={s.chatSize} onChange={(v) => setSetting("chatSize", v)} />
        </Row>
        <Row label="送信キー">
          <Choice
            value={s.mirrorSend}
            options={MIRROR_SEND_MODES.map((m) => [m.id, m.label])}
            onChange={(v) => setSetting("mirrorSend", v)}
          />
        </Row>
        <p className="muted ds-note">
          {s.mirrorSend === "enter"
            ? "Enter で送信、Shift+Enter で改行。"
            : "Ctrl+Enter（⌘+Enter）で送信、Enter で改行。スマホ向け。"}
        </p>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">朗読ビュー</h4>
        <Row label="フォント">
          <FontSelect value={s.readerFont} onChange={(v) => setSetting("readerFont", v)} fonts={READER_FONTS} stack={readerFontStack} />
        </Row>
        <Row label="文字サイズ">
          <Stepper value={s.readerSize} onChange={(v) => setSetting("readerSize", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">ファイルアイコン</h4>
        <Row label="アイコンセット">
          <ChipChoice
            value={s.iconSet}
            options={ICON_SETS.map((x) => [x.id, x.label])}
            onChange={(v) => setSetting("iconSet", v)}
          />
        </Row>
        <Row label="プレビュー">
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
        プレビュー: <span style={{ fontFamily: fontStack(s.viewerFont) }}>const x = (a) =&gt; a !== 0;</span>
      </p>
    </div>
  );
}

function Row({ label, children }: { label: ReactNode; children?: ReactNode }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}

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
          {f}
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
function Stepper({
  value,
  onChange,
  min = 9,
  max = 28,
}: {
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
}) {
  const set = (n: number) => onChange(Math.min(max, Math.max(min, n)));
  return (
    <div className="stepper">
      <button type="button" onClick={() => set(value - 1)} disabled={value <= min} aria-label="小さく">
        −
      </button>
      <span className="stepper-val">{value}px</span>
      <button type="button" onClick={() => set(value + 1)} disabled={value >= max} aria-label="大きく">
        ＋
      </button>
    </div>
  );
}
