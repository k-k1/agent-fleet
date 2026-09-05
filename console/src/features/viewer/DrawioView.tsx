// DrawioView — pane that renders a `.drawio` file as a diagram (docs/log/65, ADR 0046).
//
// The drawing itself is done by the bundled drawio viewer inside an iframe (drawioFrame.ts). This
// component only fetches and hands over. The frame never fetches anything itself, so every
// request happens here:
//   1. The diagram XML comes from `api/fs/download`. `api/fs/file` truncates at 2 MiB
//      (maxEditorFileBytes), which turns a diagram with embedded images into
//      "(file too large...)". download has no size limit.
//   2. The 4 MB viewer source is fetched here as text. It must not be loaded by the frame with
//      `<script src>`: a request from an origin-less frame counts as cross-site, so the
//      SameSite=Lax session cookie is not sent and the CP's authGate answers 401 (§65.11-7).
//      The parent, which holds the credentials, fetches it and passes the body in.
//   3. Send only after the frame says `ready`. Posting right after creating the iframe delivers
//      the message to the initial about:blank, before the srcdoc document exists, and it is lost
//      (measured).
//
// Rebuild the whole frame when the theme changes (docs/log/65 §65.11-12). Asking the same frame to
// redraw does not switch drawio's theme: measured, only the background and fills went dark, while
// container headings disappeared and edge labels kept their light-theme white pill with black
// text. Colour choices are frozen at load and first render; a theme round trip within one document
// is not supported. Rebuilding costs only a re-evaluation of the 4 MB source (~76ms from cache),
// and the view position (page, zoom, offset) is carried over, so it feels continuous.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import viewerAssetUrl from "../../../vendor/drawio/viewer-static.min.js?url";
import { downloadURL, rel } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { DRAWIO_MSG, drawioFrameSrcdoc, isDrawioFrameEvent, type DrawioViewState } from "./drawioFrame.ts";

export interface DrawioState {
  pages: number;
  page: number;
  scale: number;
}

// The viewer source only needs fetching once per app (it is a hashed asset, so the browser cache
// helps too). A failed promise is not kept, so the next pane retries.
let viewerSourcePromise: Promise<string> | null = null;

function viewerSource(): Promise<string> {
  if (!viewerSourcePromise) {
    viewerSourcePromise = fetch(new URL(viewerAssetUrl, document.baseURI).href, {
      credentials: "same-origin",
    })
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`viewer ${r.status}`))))
      .catch((e) => {
        viewerSourcePromise = null;
        throw e;
      });
  }
  return viewerSourcePromise;
}

interface DrawioViewProps {
  filePath: string;
  dark: boolean;
  /** State behind the header's "n / m" and zoom percentage. null when the file could not be read. */
  onState?: (state: DrawioState | null) => void;
  /** Hook that offers a "view source" route when the file cannot be read as a diagram. */
  onShowSource?: () => void;
}

export function DrawioView({ filePath, dark, onState, onShowSource }: DrawioViewProps) {
  const tr = useT();
  const frameRef = useRef<HTMLIFrameElement>(null);
  const [xml, setXml] = useState<string | null>(null);
  const [booted, setBooted] = useState(false);
  // Last known view position, handed straight back when the frame is rebuilt.
  const viewStateRef = useRef<DrawioViewState | null>(null);
  const [err, setErr] = useState("");
  const [frameErr, setFrameErr] = useState("");
  const onStateRef = useRef(onState);
  onStateRef.current = onState;

  // The srcdoc is built per theme. Since the point is to rebuild on a theme change, the iframe
  // gets a key so React creates a new element: swapping srcDoc on the same element can leave us
  // holding a window that still carries the previous document's listeners.
  const srcdoc = useMemo(() => drawioFrameSrcdoc({ dark }), [dark]);

  const post = useCallback((message: Record<string, unknown>) => {
    const win = frameRef.current?.contentWindow;
    if (!win) return;
    win.postMessage({ af: DRAWIO_MSG, ...message }, "*");
  }, []);

  // A new frame starts knowing nothing, so boot runs again from the top.
  useEffect(() => {
    setBooted(false);
  }, [dark]);

  useEffect(() => {
    let alive = true;
    setXml(null);
    setErr("");
    setFrameErr("");
    // Drop the view position when the file changes: coordinates from another document mean nothing.
    viewStateRef.current = null;
    onStateRef.current?.(null);
    fetch(downloadURL(filePath))
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
      .then((text) => alive && setXml(text))
      .catch(() => alive && setErr(tr("view.cannot_load")));
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filePath]);

  // Events from the frame. A sandboxed frame's origin is opaque ("null"), so origin cannot filter
  // them; match on the sending window instead.
  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (event.source !== frameRef.current?.contentWindow) return;
      if (!isDrawioFrameEvent(event.data)) return;
      const msg = event.data;
      if (msg.t === "ready") {
        // The document exists now; only here is the viewer source handed over.
        viewerSource()
          .then((src) => post({ t: "boot", src }))
          .catch(() => setFrameErr(tr("view.drawio.viewer_unavailable")));
        return;
      }
      if (msg.t === "booted") {
        setBooted(true);
        return;
      }
      if (msg.t === "error") {
        onStateRef.current?.(null);
        setFrameErr(
          msg.code === "boot"
            ? tr("view.drawio.viewer_unavailable")
            : msg.code === "empty"
              ? tr("view.drawio.empty")
              : tr("view.drawio.unreadable"),
        );
        return;
      }
      if (msg.t === "stencils") {
        // Fetch the vendor-icon stencils the frame asked for from the CP and pass them in
        // (docs/log/65 §65.5). The frame must not fetch them: with no origin the cookie is not
        // sent and authGate answers 401 (the same hole as §65.11-7, measured).
        //
        // Anything that fails is dropped silently: in a closed network only the artwork is
        // missing while shapes, colours and labels remain — the same picture as before stencils
        // existed. It must not be surfaced as an error, since the diagram itself opened fine.
        // Build the URL with rel(): a bare relative path resolves against the document URL, which
        // points somewhere else behind a proxy that strips a path such as `/agent-fleet/`, or on a
        // deep `/open/...` URL (the same trap recorded for the viewer asset in §65.7).
        Promise.all(
          msg.sets.map((name) =>
            fetch(rel(`api/drawio/stencils/${name.split("/").map(encodeURIComponent).join("/")}`), {
              credentials: "same-origin",
            })
              .then((r) => (r.ok ? r.text() : null))
              .catch(() => null),
          ),
        ).then((xmls) => {
          const got = xmls.filter((x): x is string => !!x);
          // Report back the names that failed, so the frame can take them off its "already asked"
          // list and request them again on the next render. Without that, a single upstream blip
          // leaves the icons missing for the rest of the pane's life.
          const missing = msg.sets.filter((_, i) => !xmls[i]);
          if (got.length || missing.length) post({ t: "stencils", xml: got, missing });
        });
        return;
      }
      setFrameErr("");
      viewStateRef.current = {
        pageId: msg.pageId,
        scale: msg.scale,
        tx: msg.tx,
        ty: msg.ty,
        adjusted: msg.adjusted,
      };
      onStateRef.current?.({ pages: msg.pages, page: msg.page, scale: msg.scale });
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [post, tr]);

  // Render request, sent once the viewer has been evaluated and the XML has arrived. The frame
  // holds one message if they arrive in the other order, but waiting here keeps a single path.
  // After a rebuild the previous view position goes along with it, to be restored.
  useEffect(() => {
    if (!booted || xml == null) return;
    post({ t: "render", xml, dark, restore: viewStateRef.current });
  }, [booted, xml, dark, post]);

  if (err) return <pre className="filebody muted">({err})</pre>;

  return (
    <div className="drawioview">
      <iframe
        // A distinct element per theme; this is what triggers the rebuild.
        key={dark ? "dark" : "light"}
        ref={frameRef}
        className="drawio-frame"
        title={tr("view.diagram")}
        // Neither allow-same-origin nor allow-popups: the first would give the frame the
        // Console's own privileges, the second would let the lightbox call window.open.
        sandbox="allow-scripts"
        srcDoc={srcdoc}
      />
      {xml == null && !err && !frameErr && <div className="drawio-note muted">…</div>}
      {frameErr && (
        <div className="drawio-note" role="status">
          {frameErr}
          {onShowSource && (
            <button type="button" onClick={onShowSource}>
              {tr("view.source")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
