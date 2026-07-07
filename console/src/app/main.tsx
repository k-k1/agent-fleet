// Entry for the NEXT console (docs/22 rebuild) — served as next.html alongside the
// frozen current console (index.html) during the parallel-entry transition.
import React from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App.tsx";
import { wireViewport } from "../viewport.ts";
import "@vscode/codicons/dist/codicon.css";
import "../styles/tokens.css";
import "../styles/base.css";
import "../ui/ui.css";
import "./app.css";

// Pin the frame's bars above the mobile soft keyboard (iOS visual-viewport fit).
wireViewport();

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
