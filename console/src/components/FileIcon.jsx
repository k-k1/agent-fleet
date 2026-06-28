import { brandIconURL, mark } from "../lib/fileicons.js";
import Icon from "./Icon.jsx";

// FileIcon renders the icon for a filename: AI/secret files get an emphasized
// codicon; files with a known type get the colorful brand SVG; everything else
// falls back to a monochrome codicon. Pairs with DirIcon for folders.
export default function FileIcon({ name }) {
  const m = mark(name);
  if (m === "ai") return <Icon name="sparkle" className="fi-ai" title="AI 関連" />;
  if (m === "secret") return <Icon name="key" className="fi-secret" title="機密" />;
  const url = brandIconURL(name);
  if (url) return <img className="fi-svg" src={url} alt="" aria-hidden="true" />;
  return <Icon name="file" className="fi-generic" />;
}

// DirIcon: open vs closed folder (monochrome codicon, matching the chrome).
export function DirIcon({ open }) {
  return <Icon name={open ? "folder-opened" : "folder"} className="fi-folder" />;
}
