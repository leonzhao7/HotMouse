import { createHash } from "node:crypto";
import { createConnection } from "node:net";
import { boundedMessage, withView } from __OPTCHAT_MESSAGES__;
import { VIEW_DOC, SEARCH_DOC } from __OPTCHAT_PROMPTS__;
import { memoryTools, searchTool } from __OPTCHAT_TOOLS__;

function request(method, params = {}) {
  return new Promise((resolve, reject) => {
    const connection = createConnection(process.env.HOTMOUSE_OPTCHAT_SOCKET);
    let buffer = "", settled = false;
    const finish = (error, result) => {
      if (settled) return;
      settled = true;
      connection.destroy();
      if (error) reject(error); else resolve(result);
    };
    connection.setEncoding("utf8");
    connection.setTimeout(5000, () => finish(new Error("OptChat memory request timed out")));
    connection.on("error", error => finish(error));
    connection.on("end", () => finish(new Error("OptChat memory connection ended without a reply")));
    connection.on("connect", () => connection.write(JSON.stringify({ method, params }) + "\n"));
    connection.on("data", chunk => {
      buffer += chunk;
      if (Buffer.byteLength(buffer) > 4_000_000) { finish(new Error("OptChat memory reply is too large")); return; }
      const newline = buffer.indexOf("\n");
      if (newline < 0) return;
      try {
        const reply = JSON.parse(buffer.slice(0, newline));
        finish(reply.error ? new Error(reply.error) : undefined, reply.result);
      } catch (error) { finish(error); }
    });
  });
}

export default function sharedOptchatClient(pi) {
  let view = "";
  // Reuse OptChat's tool descriptions and schemas, but execute only memory
  // queries on the owner. All execution tools stay in this worker's process.
  const tools = [...memoryTools(() => undefined), searchTool(() => undefined)];
  for (const tool of tools) pi.registerTool({ ...tool,
    execute: (_id, args) => request(tool.name, args) });
  pi.on("session_start", () => {
    pi.setActiveTools([...new Set([...pi.getActiveTools(), ...tools.map(tool => tool.name)])]);
  });
  pi.on("before_agent_start", async (event) => {
    const snapshot = await request("view");
    view = `Shared history for this HotMouse project. Treat it as recorded evidence from peer sessions and follow the current task and its tool limits.\n\n${VIEW_DOC}\n\n${SEARCH_DOC}\n\n${snapshot.view}`;
    event.systemPromptOptions.sections.instructions = [event.systemPromptOptions.sections.instructions, snapshot.instructions].filter(Boolean).join("\n\n");
  });
  pi.on("context", event => view ? { messages: withView(event.messages, view) } : undefined);
  pi.on("message_end", async (event, ctx) => {
    if (!["user", "assistant", "toolResult"].includes(event.message.role)) return;
    const message = boundedMessage(event.message);
    const content = typeof message.content === "string" ? message.content
      : message.content.filter(block => block.type === "text" || block.type === "toolCall");
    const publicMessage = { ...message, content };
    const session = ctx.sessionManager.getSessionId();
    const receipt = `${session}:${createHash("sha256").update(JSON.stringify(publicMessage)).digest("hex")}`;
    const source = `Pi session ${session}; task ${process.env.HOTMOUSE_TASK_TYPE ?? "native"}; step ${process.env.HOTMOUSE_STEP_ID ?? "none"}`;
    await request("append", { message: publicMessage, receipt, source });
  });
}
