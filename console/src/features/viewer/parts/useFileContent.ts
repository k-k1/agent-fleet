// The content of the open file, plus the display state that is reset along with it when a
// different file is opened.
//
// It is one hook because the reset scope belongs to the load effect: opening another file
// must discard not just the content but the image dimensions, the image mode, the PDF page
// count and the external-change notice at the same time. Split them apart and "which one did
// we forget to reset" is no longer visible from outside the effect.
//
// These two effects must stay the first effects to run on this surface; do not move the
// declaration in FileView.
import { useEffect, useRef, useState } from "react";
import { api, isTransientErr } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { LineMarks } from "../CodeView.tsx";

export interface FileData {
  error?: { message?: string };
  path?: string;
  binary?: boolean;
  content?: string;
  size?: number;
  truncated?: boolean;
  lfs?: boolean;
  editable?: boolean;
  editabilityReason?: string | null;
  revision?: string;
}

export function useFileContent(filePath: string) {
  const tr = useT();
  const [data, setData] = useState<FileData | null>(null);
  const dataRef = useRef<FileData | null>(null);
  dataRef.current = data;
  // External-change notice for panes without an editor buffer (docs/log/44 §7.4's
  // read-only view case); buffered panes speak through the editor status line.
  const [viewNotice, setViewNotice] = useState("");
  const [err, setErr] = useState("");
  const [imgMode, setImgMode] = useState<"preview" | "source">("preview");
  const [imgDims, setImgDims] = useState<{ w: number; h: number } | null>(null);
  // PDF page count for the info bar; null until the document opens, shown where the line
  // count would be.
  const [pdfPages, setPdfPages] = useState<number | null>(null);
  const [marks, setMarks] = useState<LineMarks | null>(null);

  // Editor-style change marks for git-tracked working-tree files.
  useEffect(() => {
    setMarks(null);
    if (!filePath || !filePath.startsWith("repos/")) return;
    let alive = true;
    api(`api/fs/linemarks?path=${encodeURIComponent(filePath)}`)
      .then((d) => alive && d && !d.error && setMarks(d))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [filePath]);

  useEffect(() => {
    if (!filePath) return;
    let alive = true;
    let timer = 0;
    let tries = 0;
    let settled = false; // a terminal result (content or a real error) has landed
    setData(null);
    setErr("");
    setViewNotice("");
    setImgDims(null);
    setImgMode("preview");
    setPdfPages(null);
    // Load the file, retrying transient gateway failures. Right after a WS start the agent
    // is briefly unreachable and api() resolves an http_5xx error (not a throw); committing
    // that as a real error would leave the pane stuck on "(…cannot load)" forever. Genuine
    // errors (missing file, permission) carry an app code and stay terminal.
    const retry = () => {
      if (!alive) return;
      const delay = Math.min(5000, 700 * 2 ** Math.min(tries, 3));
      tries++;
      timer = window.setTimeout(load, delay);
    };
    const load = () => {
      api(`api/fs/file?path=${encodeURIComponent(filePath)}`)
        .then((d) => {
          if (!alive) return;
          if (isTransientErr(d)) return retry();
          settled = true;
          if (d && d.error) setErr(d.error.message || tr("view.cannot_load"));
          else setData(d);
        })
        .catch(() => alive && retry());
    };
    const onVis = () => {
      if (!document.hidden && alive && !settled) {
        tries = 0;
        window.clearTimeout(timer);
        load();
      }
    };
    load();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      alive = false;
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVis);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filePath]);

  return {
    data,
    setData,
    dataRef,
    err,
    viewNotice,
    setViewNotice,
    imgMode,
    setImgMode,
    imgDims,
    setImgDims,
    pdfPages,
    setPdfPages,
    marks,
  };
}
