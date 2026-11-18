import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import {
  api,
  ApiError,
  projectPath,
  queryString,
  workerPayload,
} from "./api.js";

const originalFetch = globalThis.fetch;
const originalDispatchEvent = globalThis.dispatchEvent;
afterEach(() => {
  globalThis.fetch = originalFetch;
  globalThis.dispatchEvent = originalDispatchEvent;
});

test("Worker PUT preserves configuration and secrets while removing deleted keys", () => {
  const config = {
    name: "local",
    driver: "command",
    command: "runner",
    enabled: false,
    workspace_root: "/work",
    context_root: "/ctx",
    completed_action: "keep",
    runtime: { recovery_attempts: 3 },
    environment: { API_TOKEN: "***", REMOVE_ME: "old", PUBLIC: "old" },
    worker_mode: "dispatcher_pool",
    worker_count: 4,
  };
  const result = workerPayload(config, [
    { key: "API_TOKEN", value: "***" },
    { key: "PUBLIC", value: "new" },
    { key: "NEW_KEY", value: "value" },
  ]);
  assert.equal(result.enabled, false);
  assert.equal(result.command, "runner");
  assert.deepEqual(result.environment, {
    API_TOKEN: "***",
    PUBLIC: "new",
    NEW_KEY: "value",
  });
  assert.deepEqual(result.environment_remove, ["REMOVE_ME"]);
  assert.equal(result.worker_mode, undefined);
  assert.equal(config.environment.PUBLIC, "old");
});
test("Worker environment prevents duplicate keys and moving unreadable secrets", () => {
  assert.throws(
    () =>
      workerPayload({ environment: {} }, [
        { key: "KEY", value: "one" },
        { key: "KEY", value: "two" },
      ]),
    /重复/,
  );
  assert.throws(
    () =>
      workerPayload({ environment: { OLD_TOKEN: "***" } }, [
        { key: "NEW_TOKEN", value: "***" },
      ]),
    /未知的掩码/,
  );
  assert.throws(
    () => workerPayload({}, [{ key: "2BAD", value: "" }]),
    /环境变量名/,
  );
});
test("Dispatcher payload preserves independent secrets and removes deleted keys", () => {
  const config = {
    environment: { WORKER_KEY: "***" },
    dispatcher: {
      driver: "pi",
      model: "planner-model",
      command: "",
      environment: { PI_API_KEY: "***", REMOVE: "old" },
    },
  };
  const result = workerPayload(
    config,
    [{ key: "WORKER_KEY", value: "***" }],
    [{ key: "PI_API_KEY", value: "***" }],
  );
  assert.deepEqual(result.dispatcher, {
    driver: "pi",
    model: "planner-model",
    command: "",
    environment: { PI_API_KEY: "***" },
    environment_remove: ["REMOVE"],
  });
  assert.deepEqual(result.environment, { WORKER_KEY: "***" });
  assert.equal(workerPayload({ dispatcher: null }, []).dispatcher, null);
});
test("API preserves core paths, cookies and structured request bodies", async () => {
  globalThis.fetch = async (path, options) => {
    assert.equal(path, "/projects/p001/steps");
    assert.equal(options.credentials, "same-origin");
    assert.equal(
      options.body,
      JSON.stringify({ from: ["origin"], description: "a\nb" }),
    );
    return new Response(JSON.stringify({ id: "s001" }));
  };
  assert.deepEqual(
    await api("/projects/p001/steps", {
      method: "POST",
      body: { from: ["origin"], description: "a\nb" },
    }),
    { id: "s001" },
  );
  assert.equal(projectPath("id/a"), "/projects/id%2Fa");
  assert.equal(
    queryString({ project_id: "p 1", severity: "", limit: 20 }),
    "?project_id=p+1&limit=20",
  );
});
test("API distinguishes service failure, JSON errors and empty responses", async () => {
  globalThis.fetch = async () =>
    new Response(JSON.stringify({ detail: "step claimed" }), { status: 409 });
  await assert.rejects(
    api("/projects"),
    (error) =>
      error instanceof ApiError &&
      error.status === 409 &&
      error.message === "step claimed",
  );
  globalThis.fetch = async () => {
    throw new TypeError("network");
  };
  await assert.rejects(api("/projects"), (error) => error.status === 0);
  globalThis.fetch = async () => new Response(null, { status: 204 });
  assert.equal(await api("/projects", { method: "DELETE" }), null);
  globalThis.fetch = async () => new Response("<html>wrong proxy</html>");
  await assert.rejects(api("/projects"), /无效的 JSON/);
});

test("Platform token failures preserve the local session while session failures require login", async () => {
  const events = [];
  globalThis.dispatchEvent = (event) => events.push(event.type);
  globalThis.fetch = async () =>
    new Response(
      JSON.stringify({
        code: "token_revoked",
        message: "platform token expired",
      }),
      { status: 401 },
    );
  await assert.rejects(
    api("/api/platforms/ctfy/challenges"),
    (error) => error.status === 401 && error.payload.code === "token_revoked",
  );
  assert.deepEqual(events, []);

  globalThis.fetch = async () =>
    new Response(JSON.stringify({ detail: "authentication required" }), {
      status: 401,
    });
  await assert.rejects(api("/api/platforms/ctfy/challenges"), /authentication/);
  await assert.rejects(api("/projects"), /authentication/);
  assert.deepEqual(events, ["hotmouse:unauthorized", "hotmouse:unauthorized"]);
  await assert.rejects(
    api("/api/auth/me", { session: false }),
    /authentication/,
  );
  assert.equal(events.length, 2);
});
