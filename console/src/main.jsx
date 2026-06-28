import React from "react";
import { createRoot } from "react-dom/client";
import { AppProvider } from "./state.jsx";
import App from "./App.jsx";
import "@vscode/codicons/dist/codicon.css";
import "./styles.css";

createRoot(document.getElementById("root")).render(
  <React.StrictMode>
    <AppProvider>
      <App />
    </AppProvider>
  </React.StrictMode>,
);
