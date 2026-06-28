// Icon renders a VS Code codicon glyph (webfont, bundled via @vscode/codicons).
// Usage: <Icon name="add" />. Color follows currentColor; size follows font-size.
// Pass spin for the loading glyph. aria-hidden because icons here are decorative;
// the surrounding button/title carries the accessible label.
export default function Icon({ name, className = "", spin = false, title }) {
  const cls = ["codicon", "codicon-" + name, spin ? "codicon-spin" : "", className]
    .filter(Boolean)
    .join(" ");
  return <i className={cls} title={title} aria-hidden="true" />;
}
