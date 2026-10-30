import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { listBrowserAttachments } from "../../features/browser/attachmentService.ts";
import { openBrowserAttachment } from "../../features/browser/attachmentAction.ts";
import type { BrowserAttachmentStatus } from "../../features/browser/attachmentController.ts";
import { useBrowserUnavailable } from "../../features/browser/availability.ts";
import { PortOpen, useDefaultPortOpen } from "../../features/preview/PortOpen.tsx";
import { useCurrentPreviewInfo, usePreviewStore } from "../../features/preview/previewState.ts";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";

// A stable empty list: a selector returning a fresh [] each time re-renders forever.
const NO_PORTS: number[] = [];

// SharedPreview is one row of GET /api/preview/shared (docs/log/81 §14.6). `running` means
// "a preview URL exists right now", not the container's own state.
type SharedPreview = {
  ownerUserKey: string;
  ownerEmail: string;
  ports: number[];
  urls: Record<string, string>;
  shareLinks: Record<string, string>;
  running: boolean;
};

// The port-preview popover body, shared by the desktop popover and the ⋯ overflow. The
// attachment / preview-host / shared-preview reads happen when pvOpen or moreOpen turns true,
// not on a poll. The two setters close whichever popover the user acted from.
export function usePortPreview(
  pvOpen: boolean,
  moreOpen: boolean,
  running: boolean,
  setPvOpen: (open: boolean) => void,
  setMoreOpen: (open: boolean) => void,
) {
  const tr = useT();
  const browserUnavailable = useBrowserUnavailable();
  // Unlisted ports the owner has open right now (control-plane/preview_grant.go).
  const tempPorts = useCurrentPreviewInfo()?.tempPorts ?? NO_PORTS;
  const [port, setPort] = useState("");
  const [previewPath, setPreviewPath] = useState("/");
  // Live Chromium attachments, read when the popover opens (no polling: this is
  // a recovery entry, and the authoritative state is the pane's own socket).
  const [attachments, setAttachments] = useState<BrowserAttachmentStatus[]>([]);
  // The issued preview subdomain (docs/log/81). It changes on every start, so re-read it
  // the moment the popover opens. On a deployment without host-based previews
  // previewDomain is empty and nothing is shown.
  const [pvHosts, setPvHosts] = useState<{
    domain: string;
    urls: Record<string, string>;
    // A pasteable link that survives restarts (docs/log/81 §14.6); present while stopped too.
    shareLinks: Record<string, string>;
    public: boolean;
    tenantShare: boolean;
  } | null>(null);
  // Previews shared by other people in the same tenant (docs/log/81 §14.6).
  const [pvShared, setPvShared] = useState<SharedPreview[]>([]);

  // Enter in the port field does what a plain click on the primary button does: the default
  // target (subdomain, else browser pane, else lightweight preview — features/preview).
  const openDefaultPort = useDefaultPortOpen(Number(port.trim()), running, previewPath.trim() || "/");
  const closePortPops = () => {
    setPvOpen(false);
    setMoreOpen(false);
  };

  // Read the live Chromium attachments when a popover that shows them opens.
  // Deliberately not polled: the pane's own socket is the authoritative state,
  // and this list only has to be right at the moment the user looks at it.
  useEffect(() => {
    if (!pvOpen && !moreOpen) return;
    if (!running) {
      setAttachments((prev) => (prev.length ? [] : prev));
      return;
    }
    let cancelled = false;
    // No attachment can exist where the runtime has no browser features; asking would only
    // collect a browser_unavailable refusal.
    if (browserUnavailable) setAttachments((prev) => (prev.length ? [] : prev));
    else
      void listBrowserAttachments().then(
        (list) => !cancelled && setAttachments(list),
        () => !cancelled && setAttachments((prev) => (prev.length ? [] : prev)),
      );
    void usePreviewStore.getState().refresh({ force: true });
    void api("api/env/ws-settings").then(
      (res: any) => {
        if (cancelled) return;
        setPvHosts(
          res && !res.error && res.previewDomain
            ? {
                domain: res.previewDomain,
                urls: res.previewUrls || {},
                shareLinks: res.previewShareLinks || {},
                public: !!res.previewPublic,
                tenantShare: !!res.previewTenantShare,
              }
            : null,
        );
      },
      () => !cancelled && setPvHosts(null),
    );
    return () => {
      cancelled = true;
    };
  }, [pvOpen, moreOpen, running, browserUnavailable]);

  // Previews shared within this tenant (docs/log/81 §14.6). Not conditioned on running:
  // what appears here belongs to OTHER people's workspaces, so whether ours is stopped is
  // irrelevant.
  useEffect(() => {
    if (!pvOpen && !moreOpen) return;
    let cancelled = false;
    void api("api/preview/shared").then(
      (res: any) => !cancelled && setPvShared(res && !res.error ? res.items || [] : []),
      () => !cancelled && setPvShared([]),
    );
    return () => {
      cancelled = true;
    };
  }, [pvOpen, moreOpen]);

  const previewPop = (
    <>
      <div className="pv-section">
        <label className="pv-label">{tr("wsbar.preview.port_label")}</label>
        <div className="pv-row">
          <input
            className="preview-port"
            type="number"
            min="1"
            max="65535"
            placeholder={tr("wsbar.preview.port_ph")}
            value={port}
            onChange={(e) => setPort(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              openDefaultPort();
              closePortPops();
            }}
            title={tr("wsbar.preview.port_hint")}
          />
          <input
            className="preview-path"
            value={previewPath}
            onChange={(e) => setPreviewPath(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              openDefaultPort();
              closePortPops();
            }}
            aria-label={tr("browser.path")}
            title={tr("wsbar.preview.path_hint")}
          />
        </div>
        <PortOpen
          variant="buttons"
          port={Number(port.trim())}
          path={previewPath.trim() || "/"}
          running={!!running}
          onOpened={closePortPops}
        />
        <div className="pv-hint">
          {browserUnavailable
            ? tr("wsbar.preview.hint_unavailable", { runtime: browserUnavailable })
            : tr("wsbar.preview.hint")}
        </div>
      </div>
      {/* The issued preview subdomain (docs/log/81). No URL is shown while stopped: the
          slug is issued per start and expires on stop, so the link would only 404 — and a
          404 comes back as a report that the feature is broken. */}
      {pvHosts && Object.keys(pvHosts.urls).length > 0 && (
        <div className="pv-section">
          <label className="pv-label">
            {tr("wsbar.preview.hosts_label")}
            {pvHosts.public && <span className="pv-public"> {tr("wsbar.preview.public_badge")}</span>}
          </label>
          {Object.keys(pvHosts.urls)
            .sort((a, b) => Number(a) - Number(b))
            .map((p) => (
              <div className="pv-row pv-actions" key={p}>
                <Button
                  className="pv-host"
                  title={pvHosts.urls[p]}
                  onClick={() => {
                    window.open(pvHosts.urls[p], "_blank", "noopener");
                    setPvOpen(false);
                    setMoreOpen(false);
                  }}
                >
                  <Icon name="globe" /> :{p}
                </Button>
                <Button
                  variant="ghost"
                  title={tr("wsbar.preview.copy_url")}
                  onClick={() => void navigator.clipboard?.writeText(pvHosts.urls[p])}
                >
                  {tr("wsbar.preview.copy")}
                </Button>
                {/* Shown only while sharing: the link that survives restarts (docs/log/81
                    §14.6). The raw URL rots on every start, so this is the one to hand
                    to someone. */}
                {pvHosts.tenantShare && pvHosts.shareLinks[p] && (
                  <Button
                    variant="ghost"
                    title={tr("wsbar.preview.copy_share_url")}
                    onClick={() =>
                      void navigator.clipboard?.writeText(window.location.origin + pvHosts.shareLinks[p])
                    }
                  >
                    {tr("wsbar.preview.copy_share")}
                  </Button>
                )}
              </div>
            ))}
          <div className="pv-hint">{tr("wsbar.preview.hosts_hint")}</div>
        </div>
      )}
      {/* Unlisted ports the owner has opened for this start. They are the owner's alone and
          end with the listener, so they are listed apart from the issued URLs above. */}
      {tempPorts.length > 0 && (
        <div className="pv-section">
          <label className="pv-label">{tr("portopen.temp_badge")}</label>
          <div className="pv-row pv-actions">
            {tempPorts.map((p) => (
              <PortOpen key={p} variant="pill" port={p} running={!!running} />
            ))}
          </div>
          <div className="pv-hint">{tr("portopen.temp_hint")}</div>
        </div>
      )}
      {/* Previews shared by other people in the same tenant (docs/log/81 §14.6). What opens
          is a stable link on the Console origin that resolves the current slug; carrying the
          raw URL around 404s the moment its owner restarts. */}
      {pvShared.length > 0 && (
        <div className="pv-section">
          <label className="pv-label">{tr("wsbar.preview.shared_label")}</label>
          {pvShared.map((s) => (
            <div className="pv-row pv-actions pv-row-shared" key={s.ownerUserKey}>
              <span className="pv-shared-owner" title={s.ownerEmail || s.ownerUserKey}>
                {s.ownerEmail || s.ownerUserKey}
              </span>
              {s.running ? (
                s.ports.map((p) => (
                  <Button
                    className="pv-host"
                    key={p}
                    title={s.shareLinks[String(p)]}
                    onClick={() => {
                      window.open(window.location.origin + s.shareLinks[String(p)], "_blank", "noopener");
                      setPvOpen(false);
                      setMoreOpen(false);
                    }}
                  >
                    <Icon name="globe" /> :{p}
                  </Button>
                ))
              ) : (
                // Keep the row while stopped; removing it would read as "not shared".
                <span className="muted">{tr("wsbar.preview.shared_stopped")}</span>
              )}
            </div>
          ))}
          <div className="pv-hint">{tr("wsbar.preview.shared_hint")}</div>
        </div>
      )}
      {/* Way back to an existing Chromium an agent attached to (docs/log/53 §53.7). The
          intended entry is the action link in the mirror; this keeps a live attachment
          reachable after the conversation scrolls past it or the pane is closed. */}
      {attachments.length > 0 && (
        <div className="pv-section">
          <label className="pv-label">{tr("wsbar.preview.attachments_label")}</label>
          {attachments.map((a) => (
            <div className="pv-row pv-actions" key={a.id}>
              <Button
                className="pv-attachment"
                onClick={() => {
                  setPvOpen(false);
                  setMoreOpen(false);
                  void openBrowserAttachment(a.id);
                }}
                title={a.url || a.title}
              >
                {a.title || tr("browser.attach.canvas")}
              </Button>
            </div>
          ))}
        </div>
      )}
    </>
  );

  return previewPop;
}
