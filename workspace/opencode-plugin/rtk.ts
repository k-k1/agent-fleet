import { execFile } from "node:child_process"

// RTK OpenCode plugin — rewrites commands to use rtk for token savings.
// Requires: rtk >= 0.23.0 in PATH.
//
// This is the opencode analog of claude's PreToolUse/Bash → `rtk hook claude`
// settings hook: it intercepts the shell tool's calls and routes the command
// through rtk so the agent sees compacted output. The entrypoint seeds it into
// ~/.config/opencode/plugin only when a vendored rtk is present in the image.
//
// Thin delegating plugin: all rewrite logic lives in `rtk rewrite`, the single
// source of truth (rtk's Rust registry). To change rewrite rules, edit rtk —
// not this file.
//
// No dependency on @opencode/plugin: opencode 2 reads `id` and `setup` from a plain
// default-exported object. The Bun `$` helper is not part of its context, so rtk runs
// through node:child_process.

// run never rejects: like the `.nothrow()` it replaces, a non-zero exit still yields its stdout.
const run = (file: string, args: string[]): Promise<{ ok: boolean; stdout: string }> =>
  new Promise((resolve) => {
    execFile(file, args, { encoding: "utf8" }, (err, stdout) => resolve({ ok: !err, stdout: String(stdout ?? "") }))
  })

const hasRtk = async (): Promise<boolean> => {
  if ((await run("which", ["rtk"])).ok) return true
  console.warn("[rtk] rtk binary not found in PATH — plugin disabled")
  return false
}

// rewrite returns the rtk form of the tool call's command, or null to leave it unchanged.
const rewrite = async (tool: unknown, args: unknown): Promise<string | null> => {
  const name = String(tool ?? "").toLowerCase()
  if (name !== "shell") return null
  if (!args || typeof args !== "object") return null
  const command = (args as Record<string, unknown>).command
  if (typeof command !== "string" || !command) return null
  const rewritten = (await run("rtk", ["rewrite", command])).stdout.trim()
  return rewritten && rewritten !== command ? rewritten : null
}

const apply = async (tool: unknown, args: unknown) => {
  const next = await rewrite(tool, args)
  if (next !== null) (args as Record<string, unknown>).command = next
}

export default {
  id: "agent-fleet.rtk",
  async setup(ctx: any) {
    if (!(await hasRtk())) return
    await ctx.tool.hook("execute.before", (event: any) => apply(event?.tool, event?.input))
  },
}
