#!/usr/bin/env node

// Minimal stdio MCP used by docs/log/53 P0. The two markers intentionally differ:
// a CLI passes structuredContent to its model only when the final answer can
// reproduce STRUCTURED_ONLY without learning it from the text fallback.
import readline from "node:readline";

const protocolVersion = "2025-06-18";
const tool = {
  name: "structured_probe",
  description: "Call once, then report every field and value returned by this tool exactly.",
  inputSchema: { type: "object", properties: {} },
  outputSchema: {
    type: "object",
    properties: {
      attachment_id: { type: "string" },
      open_url: { type: "string" },
    },
    required: ["attachment_id", "open_url"],
  },
};

function result(id, value) {
  return { jsonrpc: "2.0", id, result: value };
}

function handle(request) {
  switch (request.method) {
    case "initialize":
      return result(request.id, {
        protocolVersion,
        capabilities: { tools: {} },
        serverInfo: { name: "structured-result-probe", version: "1" },
      });
    case "ping":
      return result(request.id, {});
    case "tools/list":
      return result(request.id, { tools: [tool] });
    case "tools/call":
      if (request.params?.name !== tool.name) {
        return result(request.id, {
          content: [{ type: "text", text: "unknown tool" }],
          isError: true,
        });
      }
      return result(request.id, {
        content: [{
          type: "text",
          text: "TEXT_FALLBACK attachment_id=ba_text_fallback (open_url is intentionally absent)",
        }],
        structuredContent: {
          attachment_id: "ba_structured_only",
          open_url: "/open/STRUCTURED_ONLY_7c91d4",
        },
      });
    default:
      if (request.id == null) return null;
      return {
        jsonrpc: "2.0",
        id: request.id,
        error: { code: -32601, message: `method not found: ${request.method}` },
      };
  }
}

const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
lines.on("line", (line) => {
  try {
    const response = handle(JSON.parse(line));
    if (response) process.stdout.write(`${JSON.stringify(response)}\n`);
  } catch (error) {
    process.stderr.write(`probe server: ${error.message}\n`);
  }
});
