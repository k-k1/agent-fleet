// Alias (ADR 0067 rule 1: do not touch a single line of the call sites).
//
// The implementation lives in parts/mcpForm.tsx. features/repos/ProjectActionPanels.tsx
// pulls Field / Meta from `../settings/mcpForm.tsx`, which is outside FE-SETTINGS's
// ownership, so this one file keeps the old path alive.
//
// Being used from outside the settings modal means this part really belongs in src/ui.
// Promoting it — and retiring this alias — is a separate PR in the next wave.
export * from "./parts/mcpForm.tsx";
