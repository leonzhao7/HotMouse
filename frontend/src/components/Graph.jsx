import { useEffect, useRef, useState } from "react";
import cytoscape from "cytoscape";
import dagre from "cytoscape-dagre";
import { Maximize, Minus, Plus } from "lucide-react";
import { useApp } from "../context.jsx";
import { buildFactGraph, fitGraph } from "../graphModel.js";
import { IconButton } from "./ui.jsx";

cytoscape.use(dagre);
export default function Graph({ detail, selection, onSelect }) {
  const container = useRef(null);
  const graph = useRef(null);
  const select = useRef(onSelect);
  select.current = onSelect;
  const { theme } = useApp();
  const [showAbandoned, setShowAbandoned] = useState(false);
  useEffect(() => {
    const dark = theme === "dark";
    const colors = {
      surface: dark ? "#132019" : "#e9f6ee",
      text: dark ? "#d2dbe6" : "#293747",
      border: dark ? "#415268" : "#a5b4c3",
      green: dark ? "#65cf85" : "#238a48",
      blue: dark ? "#81b5ff" : "#3779bc",
      amber: dark ? "#dfb66c" : "#b98324",
    };
    const cy = cytoscape({
      container: container.current,
      elements: [],
      minZoom: 0.15,
      maxZoom: 2.4,
      style: [
        {
          selector: "node",
          style: {
            label: "data(label)",
            "font-size": 11,
            "font-family": "system-ui, sans-serif",
            color: colors.text,
            "text-wrap": "wrap",
            "text-max-width": 145,
            "text-valign": "center",
            "text-halign": "center",
            width: 160,
            height: 60,
            shape: "roundrectangle",
            "background-color": colors.surface,
            "border-color": colors.border,
            "border-width": 1.3,
          },
        },
        {
          selector: 'node[kind="origin"]',
          style: {
            width: 95,
            height: 45,
            "border-color": colors.green,
            color: colors.green,
          },
        },
        {
          selector: 'node[kind="goal"]',
          style: {
            shape: "round-diamond",
            height: 92,
            width: 170,
            "border-color": colors.blue,
          },
        },
        {
          selector: 'node[kind="goal"][status="completed"]',
          style: { "border-color": colors.green },
        },
        {
          selector: 'node[kind="step"]',
          style: {
            shape: "ellipse",
            width: 42,
            height: 42,
            "font-size": 10,
            "border-style": "dashed",
            "border-color": colors.amber,
          },
        },
        {
          selector: 'node[status="working"]',
          style: {
            "border-color": colors.blue,
            "border-style": "solid",
            "border-width": 2.5,
          },
        },
        { selector: 'node[status="abandoned"]', style: { opacity: 0.45 } },
        {
          selector: "edge",
          style: {
            label: "data(label)",
            width: 1.2,
            "line-color": colors.border,
            "target-arrow-color": colors.border,
            "target-arrow-shape": "triangle",
            "curve-style": "bezier",
            "font-size": 9,
            color: colors.text,
            "text-background-color": dark ? "#0e151e" : "#f5f7fa",
            "text-background-opacity": 0.9,
            "text-background-padding": "3px",
            "text-rotation": "autorotate",
          },
        },
        {
          selector: 'edge[kind="scope"], edge[kind="goal"]',
          style: {
            "line-style": "dashed",
            "line-color": colors.blue,
            "target-arrow-color": colors.blue,
            "font-size": 8,
          },
        },
        {
          selector: 'edge[status="concluded"], edge[kind="evidence"]',
          style: {
            "line-color": colors.green,
            "target-arrow-color": colors.green,
          },
        },
        {
          selector: 'edge[status="working"]',
          style: {
            "line-color": colors.blue,
            "target-arrow-color": colors.blue,
            width: 2,
          },
        },
        {
          selector: ":selected",
          style: {
            "border-color": colors.green,
            "border-width": 3,
            "line-color": colors.green,
            "target-arrow-color": colors.green,
          },
        },
      ],
    });
    graph.current = cy;
    cy.on("tap", "node, edge", (event) => {
      const data = event.target.data();
      if (data.entityId)
        select.current({ type: data.entityType, id: data.entityId });
    });
    const observer = new ResizeObserver(() => {
      cy.resize();
      fitGraph(cy);
    });
    observer.observe(container.current);
    return () => {
      observer.disconnect();
      cy.destroy();
      graph.current = null;
    };
  }, [theme]);
  useEffect(() => {
    const cy = graph.current;
    const elements = buildFactGraph(detail, { showAbandoned });
    const previous = cy
      .elements()
      .map((element) => element.id())
      .sort()
      .join(",");
    const current = elements
      .map((element) => element.data.id)
      .sort()
      .join(",");
    if (previous === current) {
      cy.batch(() => {
        elements.forEach((element) =>
          cy.getElementById(element.data.id).data(element.data),
        );
      });
    } else {
      cy.elements().remove();
      cy.add(elements);
      cy.layout({
        name: "dagre",
        rankDir: "LR",
        nodeSep: 45,
        rankSep: 130,
        edgeSep: 24,
        animate: false,
        fit: false,
      }).run();
      fitGraph(cy);
    }
    cy.elements().unselect();
    if (selection)
      cy.elements()
        .filter(
          (element) =>
            element.data("entityId") === selection.id &&
            element.data("entityType") === selection.type,
        )
        .select();
  }, [detail, showAbandoned, theme, selection]);
  return (
    <div className="graph-wrap">
      <div className="graph-tools">
        <label>
          <input
            type="checkbox"
            checked={showAbandoned}
            onChange={(event) => setShowAbandoned(event.target.checked)}
          />
          显示已放弃步骤
        </label>
        <div>
          <IconButton
            icon={Plus}
            label="放大图形"
            onClick={() =>
              graph.current.zoom({
                level: Math.min(2.4, graph.current.zoom() * 1.2),
                renderedPosition: {
                  x: graph.current.width() / 2,
                  y: graph.current.height() / 2,
                },
              })
            }
          />
          <IconButton
            icon={Minus}
            label="缩小图形"
            onClick={() =>
              graph.current.zoom({
                level: Math.max(0.15, graph.current.zoom() / 1.2),
                renderedPosition: {
                  x: graph.current.width() / 2,
                  y: graph.current.height() / 2,
                },
              })
            }
          />
          <IconButton
            icon={Maximize}
            label="适应图形"
            onClick={() => fitGraph(graph.current)}
          />
        </div>
      </div>
      <div
        ref={container}
        className="graph-canvas"
        role="img"
        aria-label={`项目关系图，${detail.facts.length} 条事实，${detail.goals.length} 个目标，${detail.steps.length} 个步骤。可通过下方其他标签页查看全部内容。`}
      />
      <div className="graph-legend">
        <span>
          <i className="fact" />
          事实
        </span>
        <span>
          <i className="goal" />
          目标
        </span>
        <span>
          <i className="step" />
          待执行步骤
        </span>
        <span>
          <i className="working" />
          执行中
        </span>
        <span>
          <i className="evidence" />
          结果 / 完成依据
        </span>
      </div>
    </div>
  );
}
