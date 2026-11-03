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
afterEach(() => {
  globalThis.fetch = originalFetch;
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
