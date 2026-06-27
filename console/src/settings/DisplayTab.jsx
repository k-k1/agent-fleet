import { useSettings, setSetting, CODE_FONTS, fontStack } from "../lib/settings.js";

// DisplayTab: font + file-viewer preferences (CodeLeaf-inspired), persisted via the
// settings store. Terminal and viewer fonts are independent.
export default function DisplayTab() {
  const s = useSettings();
  return (
    <div className="display-settings">
      <section className="ds-group">
        <h4 className="ds-title">ターミナル</h4>
        <Row label="フォント">
          <FontSelect value={s.termFont} onChange={(v) => setSetting("termFont", v)} />
        </Row>
        <Row label="文字サイズ">
          <SizeInput value={s.termSize} onChange={(v) => setSetting("termSize", v)} />
        </Row>
      </section>

      <section className="ds-group">
        <h4 className="ds-title">ファイルビュアー</h4>
        <Row label="フォント">
          <FontSelect value={s.viewerFont} onChange={(v) => setSetting("viewerFont", v)} />
        </Row>
        <Row label="文字サイズ">
          <SizeInput value={s.viewerSize} onChange={(v) => setSetting("viewerSize", v)} />
        </Row>
        <Row label="行番号">
          <Toggle checked={s.lineNumbers} onChange={(v) => setSetting("lineNumbers", v)} />
        </Row>
        <Row label="折り返し">
          <Toggle checked={s.wrap} onChange={(v) => setSetting("wrap", v)} />
        </Row>
        <Row label="ミニマップ">
          <Toggle checked={s.minimap} onChange={(v) => setSetting("minimap", v)} />
        </Row>
        <Row label="タブ幅">
          <select value={s.tabSize} onChange={(e) => setSetting("tabSize", +e.target.value)}>
            {[2, 4, 8].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
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
    <label className="ds-row">
      <span className="ds-label">{label}</span>
      {children}
    </label>
  );
}

function FontSelect({ value, onChange }) {
  return (
    <select value={value} onChange={(e) => onChange(e.target.value)} style={{ fontFamily: fontStack(value) }}>
      {CODE_FONTS.map((f) => (
        <option key={f} value={f} style={{ fontFamily: fontStack(f) }}>
          {f}
        </option>
      ))}
    </select>
  );
}

function SizeInput({ value, onChange }) {
  return (
    <input
      type="number"
      min={9}
      max={28}
      value={value}
      onChange={(e) => onChange(Math.min(28, Math.max(9, +e.target.value || 13)))}
      style={{ width: 64 }}
    />
  );
}

function Toggle({ checked, onChange }) {
  return <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />;
}
