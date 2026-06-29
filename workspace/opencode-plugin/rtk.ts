import type { Plugin } from "@opencode-ai/plugin"

// RTK OpenCode plugin — rewrites commands to use rtk for token savings.
// Requires: rtk >= 0.23.0 in PATH.
//
// This is the opencode analog of claude's PreToolUse/Bash → `rtk hook claude`
// settings hook: it intercepts bash/shell tool calls and routes the command
// through rtk so the agent sees compacted output. The entrypoint seeds it into
// ~/.config/opencode/plugin only when a vendored rtk is present in the image.
//
// Thin delegating plugin: all rewrite logic lives in `rtk rewrite`, the single
// source of truth (rtk's Rust registry). To change rewrite rules, edit rtk —
// not this file. Vendored verbatim from `rtk init -g --opencode`.

export const RtkOpenCodePlugin: Plugin = async ({ $ }) => {
  try {
    await $`which rtk`.quiet()
  } catch {
    console.warn("[rtk] rtk binary not found in PATH — plugin disabled")
    return {}
  }

  return {
    "tool.execute.before": async (input, output) => {
      const tool = String(input?.tool ?? "").toLowerCase()
      if (tool !== "bash" && tool !== "shell") return
      const args = output?.args
      if (!args || typeof args !== "object") return

      const command = (args as Record<string, unknown>).command
      if (typeof command !== "string" || !command) return

      try {
        const result = await $`rtk rewrite ${command}`.quiet().nothrow()
        const rewritten = String(result.stdout).trim()
        if (rewritten && rewritten !== command) {
          ;(args as Record<string, unknown>).command = rewritten
        }
      } catch {
        // rtk rewrite failed — pass through unchanged
      }
    },
  }
}
