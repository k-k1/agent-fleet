import {
  useSettings,
  setSetting,
  CODE_FONTS,
  fontStack,
  ICON_SETS,
  THEMES,
  SURFACE_COLORS,
  surfaceValue,
  MIRROR_SEND_MODES,
} from "../lib/settings.js";
import FileIcon from "../components/FileIcon.jsx";

// DisplayTab: font + file-viewer preferences (CodeLeaf-inspired), persisted via the
// settings store. Every control is a horizontal selection (segmented buttons /
// chips / stepper) for a consistent feel. Terminal and viewer fonts are separate.
export default function DisplayTab() {
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
        <Row label="上部バーの背景">
          <SwatchChoice theme={s.theme} value={s.topbarColor} onChange={(v) => setSetting("topbarColor", v)} />
        </Row>
        <Row label="左ペインの背景">
          <SwatchChoice theme={s.theme} value={s.leftpaneColor} onChange={(v) => setSetting("leftpaneColor", v)} />
        </Row>
        <Row label="ファイルビュアーの背景">
          <SwatchChoice theme={s.theme} value={s.viewerColor} onChange={(v) => setSetting("viewerColor", v)} />
        </Row>
        <Row label="チャットの背景">
          <SwatchChoice theme={s.theme} value={s.chatColor} onChange={(v) => setSetting("chatColor", v)} />
        </Row>
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
        <h4 className="ds-title">チャット（Markdownビュー）</h4>
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

function Row({ label, children }) {
  return (
    <div className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </div>
  );
}

// FontSelect lays the choices out horizontally, each rendered in its own font so
// the user can compare them at a glance.
function FontSelect({ value, onChange }) {
  return (
    <div className="font-choices">
      {CODE_FONTS.map((f) => (
        <button
          key={f}
          type="button"
          className={"font-chip" + (f === value ? " active" : "")}
          style={{ fontFamily: fontStack(f) }}
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
function ChipChoice({ value, options, onChange }) {
  return (
    <div className="font-choices">
      {options.map(([v, label]) => (
        <button
          key={String(v)}
          type="button"
          className={"font-chip" + (v === value ? " active" : "")}
          onClick={() => onChange(v)}
        >
          {label}
        </button>
      ))}
    </div>
  );
}

// Choice is a small horizontal segmented control.
function Choice({ value, options, onChange }) {
  return (
    <div className="seg choice-seg">
      {options.map(([v, label]) => (
        <button
          key={String(v)}
          type="button"
          className={"seg-btn" + (v === value ? " active" : "")}
          onClick={() => onChange(v)}
        >
          {label}
        </button>
      ))}
    </div>
  );
}

// SwatchChoice picks a surface (top bar / left pane) color. Each swatch previews the
// color as it'll look in the active theme; "デフォルト" shows a slashed neutral chip.
function SwatchChoice({ theme, value, onChange }) {
  return (
    <div className="swatch-row">
      {SURFACE_COLORS.map((c) => {
        const col = surfaceValue(c.id, theme);
        return (
          <button
            key={c.id}
            type="button"
            title={c.label}
            className={"swatch" + (c.id === value ? " active" : "") + (col ? "" : " swatch-default")}
            style={col ? { background: col } : undefined}
            onClick={() => onChange(c.id)}
          >
            {c.id === value ? "✓" : ""}
          </button>
        );
      })}
    </div>
  );
}

function OnOff({ value, onChange }) {
  return (
    <Choice
      value={!!value}
      options={[[true, "オン"], [false, "オフ"]]}
      onChange={onChange}
    />
  );
}

// Stepper keeps font size button-driven but allows any size in range.
function Stepper({ value, onChange, min = 9, max = 28 }) {
  const set = (n) => onChange(Math.min(max, Math.max(min, n)));
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
