import { chmodSync, existsSync } from "node:fs";
import { createServer } from "node:net";
import { Memory } from __OPTCHAT_MEMORY__;
import { createCompressor } from __OPTCHAT_COMPACTOR__;
import { memoryDirectory, pendingImport } from __OPTCHAT_IMPORT__;
import { said } from __OPTCHAT_MESSAGES__;
import { memoryTools, searchTool } from __OPTCHAT_TOOLS__;
import { storeFor } from __OPTCHAT_STORE__;
import { checkSocketPath, createProfile, holdProfile, instructions, loadConfig,
  profilePath, removeStaleSocket, saveConfig } from __OPTCHAT_PROFILES__;

// Only memory belongs to this process. Workers keep their own native Pi
// sessions, model, environment, tools, event stream and cancellation lifetime.
export default function sharedOptchatMemory(pi) {
  let memory, unlock, server;
  const connections = new Set();
  const work = new Set();
  const warn = (text) => process.stderr.write(`Pi OptChat memory: ${text}\n`);

  pi.on("session_start", async (_event, ctx) => {
    const profile = __OPTCHAT_PROFILE__;
    const directory = profilePath(profile);
    const socket = __OPTCHAT_SOCKET__;
    if (!existsSync(directory)) {
      createProfile(profile);
      if (ctx.model) {
        const config = loadConfig(directory);
        const model = { provider: ctx.model.provider, model: ctx.model.id, thinking: pi.getThinkingLevel() };
        saveConfig(directory, { ...config, compactor: model, subagent: model });
      }
    }
    if (pendingImport(directory)) throw new Error("OptChat profile has a pending import");
    const source = memoryDirectory(directory);
    unlock = await holdProfile(storeFor(source), directory, profile, `HotMouse memory · PID ${process.pid}`, warn);
    try {
      const config = loadConfig(directory);
      memory = await Memory.open(source, createCompressor(ctx.modelRegistry, () => config.compactor,
        { accepted: () => config.summaryAcceptBytes }), { warn, jobs: config.jobs });
      const receipts = new Set(memory.root.flatMap(entry => entry.receipt ? [entry.receipt] : []));
      const tools = new Map([...memoryTools(() => memory), searchTool(() => memory)].map(tool => [tool.name, tool]));
      const request = async ({ method, params = {} }) => {
        if (method === "view") return { view: memory.render(), instructions: instructions(directory) };
        if (method === "append") {
          const { message, receipt, source } = params;
          if (!message || !["user", "assistant", "toolResult"].includes(message.role)
            || !Number.isFinite(message.timestamp) || typeof receipt !== "string" || !receipt
            || typeof source !== "string" || source.length > 256)
            throw new Error("Invalid memory message");
          if (!receipts.has(receipt)) {
            // Concurrent sessions can interleave their messages. Keep the
            // originating task with each entry, including individual tools.
            const date = new Date(message.timestamp).toISOString();
            for (const [kind, text] of said(message)) memory.append(kind, `${text}\n\n[${source}]`, date, receipt);
            if (memory.saveError) throw new Error(memory.saveError);
            receipts.add(receipt);
          }
          return null;
        }
        const tool = tools.get(method);
        if (!tool) throw new Error("Unknown memory request");
        return tool.execute("hotmouse-memory", params);
      };
      checkSocketPath(socket);
      removeStaleSocket(socket);
      server = createServer(connection => {
        connections.add(connection);
        connection.setEncoding("utf8");
        connection.on("error", () => {});
        connection.on("close", () => connections.delete(connection));
        let buffer = "", queue = Promise.resolve();
        connection.on("data", chunk => {
          buffer += chunk;
          if (Buffer.byteLength(buffer) > 4_000_000) { connection.destroy(); return; }
          let newline;
          while ((newline = buffer.indexOf("\n")) >= 0) {
            const line = buffer.slice(0, newline);
            buffer = buffer.slice(newline + 1);
            const operation = queue.then(async () => {
              let reply;
              try { reply = { result: await request(JSON.parse(line)) }; }
              catch (error) { reply = { error: error instanceof Error ? error.message : String(error) }; }
              if (!connection.destroyed) connection.end(JSON.stringify(reply) + "\n");
            });
            work.add(operation);
            void operation.finally(() => work.delete(operation)).catch(() => {});
            queue = operation.catch(() => {});
          }
        });
      });
      await new Promise((resolve, reject) => {
        server.once("error", reject);
        server.listen(socket, () => { server.off("error", reject); resolve(); });
      });
      chmodSync(socket, 0o600);
    } catch (error) {
      await memory?.close();
      await unlock();
      unlock = undefined;
      throw error;
    }
  });

  pi.on("session_shutdown", async () => {
    const closed = server && new Promise(resolve => server.close(resolve));
    for (const connection of connections) connection.destroy();
    await closed;
    await Promise.allSettled(work);
    await memory?.close();
    await unlock?.();
  });
}
