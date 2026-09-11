import { resolveIcon, mark } from "../lib/fileicons.js";
import { useSettings } from "../lib/settings.js";
import { useT } from "../lib/i18n/index.ts";
import { Icon } from "./Icon.tsx";

// FileIcon renders the icon for a filename in the user's chosen set (Display
// settings → iconSet): AI/secret files get an emphasized codicon; files with a
// known type get the brand SVG (full-color, or tinted for monochrome sets like
// Seti / Devicon mono logos via CSS mask); everything else falls back to a
// monochrome codicon. Pairs with DirIcon for folders.
interface FileIconProps {
  name: string;
}

export default function FileIcon({ name }: FileIconProps) {
  const tr = useT();
  const { iconSet } = useSettings();
  const m = mark(name);
  if (m === "ai") return <Icon name="sparkle" className="fi-ai" title={tr("ui.ai_related")} />;
  if (m === "secret") return <Icon name="key" className="fi-secret" title={tr("ui.secret")} />;
  const r = resolveIcon(iconSet, name);
  if (!r) return <Icon name="file" className="fi-generic" />;
  if (r.tint === "mask") {
    // Tint a monochrome SVG: use it as a mask and fill with the type color. url() is QUOTED:
    // these SVGs are under Vite's inline limit, so `r.url` is a data: URI whose attribute
    // quotes Vite leaves as `'`, which an unquoted url() token may not contain. Unquoted the
    // browser drops the declaration and the icon becomes a solid colored square — measured in
    // headless Chromium on all 27 inlined Seti icons and devicon's 4 mono ones.
    const url = `url("${r.url}")`;
    return (
      <span
        className="fi-mask"
        style={{ maskImage: url, WebkitMaskImage: url, backgroundColor: r.color }}
        aria-hidden="true"
      />
    );
  }
  return <img className="fi-svg" src={r.url} alt="" aria-hidden="true" />;
}

// DirIcon: open vs closed folder (monochrome codicon, matching the chrome).
export function DirIcon({ open }: { open: boolean }) {
  return <Icon name={open ? "folder-opened" : "folder"} className="fi-folder" />;
}
