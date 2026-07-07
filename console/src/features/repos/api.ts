// Repos feature endpoints. During the transition the implementations live in
// src/api.ts (shared with the frozen console); this module is the feature's
// import surface, absorbed at swap time (P8) like core/api/client.ts.
export { repoPromptTemplates } from "../../api.ts";
export type { PromptTemplateGroup, PromptTemplateItem } from "../../api.ts";
