import assert from "node:assert/strict";
import { test } from "node:test";
import { buildFactGraph } from "./graphModel.js";
import { notificationHref, routeFromHash, stepStatus } from "./utils.js";

test("graph links completed steps to facts and goals to completion evidence without rewriting data", () => {
  const detail = {
    facts: [
      { id: "origin", description: "target" },
      { id: "f001", description: "evidence" },
    ],
    goals: [
      { id: "g001", description: "goal", status: "completed", from: ["f001"] },
      { id: "g002", parent_goal_id: "g001", description: "child", from: [] },
    ],
    steps: [
      {
        id: "s001",
        description: "test",
        from: ["origin"],
        to: "f001",
        goal_id: "g001",
      },
      { id: "s002", description: "next", from: ["f001"], worker: "local" },
      {
        id: "s003",
        description: "invalid reference",
        from: ["missing"],
        abandoned: true,
      },
    ],
  };
  const copy = JSON.stringify(detail);
  const graph = buildFactGraph(detail);
  const nodes = graph.filter((element) => !element.data.source);
  assert.equal(
    nodes.some((node) => node.data.id === "s001"),
    false,
  );
  assert.equal(
    nodes.find((node) => node.data.id === "s002").data.status,
    "working",
  );
  assert.equal(
    graph.find((element) => element.data.id === "step:s001:origin").data.target,
    "f001",
  );
  assert.equal(
    graph.find((element) => element.data.id === "goal-evidence:g001:f001").data
      .source,
    "f001",
  );
  assert.equal(
    graph.find((element) => element.data.id === "goal-parent:g002").data.source,
    "g001",
  );
  assert.equal(
    graph.some((element) => element.data.source === "missing"),
    false,
  );
  assert.equal(
    buildFactGraph(detail, { showAbandoned: false }).some(
      (element) => element.data.entityId === "s003",
    ),
    false,
  );
  assert.equal(JSON.stringify(detail), copy);
});
test("step state prioritizes conclusions, then abandonment, then ownership", () => {
  assert.equal(
    stepStatus({ to: "f001", abandoned: true, worker: "local" }),
    "concluded",
  );
  assert.equal(stepStatus({ abandoned: true, worker: "local" }), "abandoned");
  assert.equal(
    stepStatus({ recovery_status: "needs_attention" }),
    "failed",
  );
  assert.equal(stepStatus({ worker: "local" }), "working");
  assert.equal(stepStatus({}), "pending");
});

test("Chinese labels wrap inside node widths without altering evidence descriptions", () => {
  const description = "确认用户能够读取另一账户的资源并保存请求与响应证据";
  const graph = buildFactGraph({
    facts: [{ id: "f001", description }],
    goals: [{ id: "g001", description, from: [] }],
    steps: [],
  });
  for (const node of graph.filter((item) => !item.data.source)) {
    assert.ok(node.data.label.includes("\n"));
    assert.ok(
      node.data.label
        .split("\n")
        .every((line) => Array.from(line).length <= 12),
    );
  }
});
test("routing survives invalid input and notification links stay inside the workbench", () => {
  const route = routeFromHash("#/projects/project%201?tab=graph");
  assert.equal(route.id, "project 1");
  assert.equal(route.params.get("tab"), "graph");
  assert.equal(routeFromHash("#/%E0%A4").page, "dashboard");
  assert.equal(notificationHref("/projects/p001"), "#/projects/p001");
  assert.equal(notificationHref("javascript:alert(1)"), null);
  assert.equal(notificationHref("https://example.com"), null);
});
