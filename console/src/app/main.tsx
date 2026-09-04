// Console entry (docs/log/22 rebuild). Loaded by index.html.
import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App.tsx";
import { ToastProvider } from "../ui/ToastProvider.tsx";
import { ConfirmProvider } from "../ui/ConfirmProvider.tsx";
import { PaneHoverProvider } from "../lib/panehover.tsx";
import { wireViewport } from "./viewport.ts";
import { registerShareSW } from "../features/memo/share.ts";
import { consumePopoutBoot } from "../features/panes/popout.ts";
import "@vscode/codicons/dist/codicon.css";
import "../styles/tokens.css";
import "../styles/base.css";
import "../ui/ui.css";
import "./app.css";
import "./topbar.css";
import "./wsbar.css";
import "../features/panes/panes.css";
import "../features/terminal/terminal.css";
import "../features/browser/browser.css";
import "../features/sessions/sessions.css";
import "../features/repos/repos.css";
import "../features/project/project.css";
import "../features/files/files.css";
import "../features/scm/scm.css";
import "../features/viewer/viewer.css";
import "../features/chat/chat.css";
import "../features/mirror/mirror.css";
import "../features/memo/memo.css";
import "../features/schedules/schedules.css";
import "../features/usage/usage.css";
import "../features/settings/settings.css";
import "../features/keys/keys.css";

// Pin the frame's bars above the mobile soft keyboard (iOS visual-viewport fit).
wireViewport();

// Pop-out tab boot (?pane=<nonce>): redeem the handoff descriptor, pin the
// tenant and set the tab mode BEFORE anything renders or fetches — synchronous
// and once-only here, so StrictMode's double effects can't re-consume it.
consumePopoutBoot();

// Install the Web Share Target service worker so the installed PWA can receive shares
// from Android's share sheet into the memo queue (docs/log/21 image attachments). Best-effort.
registerShareSW();

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ToastProvider>
      <ConfirmProvider>
        <PaneHoverProvider>
          <App />
        </PaneHoverProvider>
      </ConfirmProvider>
    </ToastProvider>
  </React.StrictMode>,
);
