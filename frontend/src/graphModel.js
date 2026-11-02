// Rabbit's fact/goal/step projection adapted for HotMouse. The graph is a view;
// it never changes protocol objects or invents facts for pending steps.
import { stepStatus, truncate } from "./utils.js";

function nodeLabel(text) {
  const summary = truncate((text || "").replace(/\s+/g, " "), 35);
  const lines = [];
  let line = "";
  let width = 0;
  for (const char of summary) {
    const size = /[^\x00-\x7F]/.test(char) ? 11 : 6;
    if (width + size > 135 && line) {
      lines.push(line);
      line = "";
      width = 0;
    }
    line += char;
    width += size;
  }
  if (line) lines.push(line);
  return lines.join("\n");
}

export function buildFactGraph(detail, { showAbandoned = true } = {}) {
  const nodes = (detail.facts || []).map((fact) => ({
    data: {
      id: fact.id,
      label: fact.id === "origin" ? "初始事实" : nodeLabel(fact.description),
      kind: fact.id === "origin" ? "origin" : "fact",
      entityType: "fact",
      entityId: fact.id,
    },
  }));
  const edges = [];
  for (const goal of detail.goals || []) {
    nodes.push({
      data: {
        id: goal.id,
        label: nodeLabel(goal.description),
        kind: "goal",
        status: goal.status,
        entityType: "goal",
        entityId: goal.id,
      },
    });
    edges.push({
      data: {
        id: `goal-parent:${goal.id}`,
        source: goal.parent_goal_id || "origin",
        target: goal.id,
        kind: "goal",
        label: goal.parent_goal_id ? "子目标" : "目标",
      },
    });
    for (const fact of goal.from || [])
      edges.push({
        data: {
          id: `goal-evidence:${goal.id}:${fact}`,
          source: fact,
          target: goal.id,
          kind: "evidence",
          label: "完成依据",
        },
      });
  }
  for (const step of detail.steps || []) {
    if (!showAbandoned && step.abandoned) continue;
    const state = stepStatus(step);
    if (!step.to)
      nodes.push({
        data: {
          id: step.id,
          label: step.id,
          kind: "step",
          status: state,
          entityType: "step",
          entityId: step.id,
        },
      });
    for (const source of step.from || [])
      edges.push({
        data: {
          id: `step:${step.id}:${source}`,
          source,
          target: step.to || step.id,
          kind: "step",
          status: state,
          label: truncate(step.description, 12),
          entityType: "step",
          entityId: step.id,
        },
      });
    if (step.goal_id)
      edges.push({
        data: {
          id: `scope:${step.id}`,
          source: step.to || step.id,
          target: step.goal_id,
          kind: "scope",
          label: "推进目标",
          entityType: "step",
          entityId: step.id,
        },
      });
  }
  const ids = new Set(nodes.map((node) => node.data.id));
  return [
    ...nodes,
    ...edges.filter(
      (edge) => ids.has(edge.data.source) && ids.has(edge.data.target),
    ),
  ];
}
export function fitGraph(cy) {
  if (!cy?.elements().length || !cy.width() || !cy.height()) return;
  cy.fit(cy.elements(), 45);
  if (cy.zoom() > 1) {
    cy.zoom(1);
    cy.center();
  }
}
