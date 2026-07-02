import React from "react";
import { createRoot } from "react-dom/client";
import { AppProvider } from "./state.jsx";
import { PaneHoverProvider } from "./lib/panehover.jsx";
import App from "./App.jsx";
import "@vscode/codicons/dist/codicon.css";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <AppProvider>
      <PaneHoverProvider>
        <App />
      </PaneHoverProvider>
    </AppProvider>
  </React.StrictMode>,
);
