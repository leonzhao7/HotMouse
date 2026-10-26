import { useMemo } from 'react'

import type { GraphNode, Step } from '@/lib/types'

interface GraphViewProps {
  goal: GraphNode
  facts: GraphNode[]
  steps: Step[]
}

interface Placed {
  node: GraphNode
  layer: number
  index: number
  count: number
}

interface Positioned extends Placed {
  x: number
  y: number
}

const NODE_WIDTH = 168
const NODE_HEIGHT = 56
const COLUMN_GAP = 96
const ROW_GAP = 24
const PADDING = 16

/**
 * 事实 -> 目标的图谱渲染。
 * 布局用确定性的分层算法: 没有入边的事实位于第 0 层，其余事实的层号为
 * 所有前驱层号的最大值加一，目标是所有指向它的事实的层号最大值加一。
 * 事实之间允许存在环，遇到环时按已分配的层号处理以保证收敛。
 */
export function GraphView({ goal, facts, steps }: GraphViewProps) {
  const { nodes, width, height, edges } = useMemo(() => {
    const factIds = new Set(facts.map((fact) => fact.id))
    const incoming = new Map<number, number[]>()
    for (const step of steps) {
      const list = incoming.get(step.to_node_id) ?? []
      list.push(step.from_node_id)
      incoming.set(step.to_node_id, list)
    }

    const layer = new Map<number, number>()
    for (const fact of facts) layer.set(fact.id, 0)

    // 迭代松弛: 事实层号 = max(前驱层号) + 1，反复直到稳定。
    for (let pass = 0; pass < facts.length + 1; pass += 1) {
      let changed = false
      for (const fact of facts) {
        const predecessors = incoming.get(fact.id) ?? []
        let best = 0
        for (const predecessor of predecessors) {
          if (!factIds.has(predecessor)) continue
          const predecessorLayer = layer.get(predecessor) ?? 0
          if (predecessorLayer > best) best = predecessorLayer
        }
        const next = predecessors.length > 0 ? best + 1 : 0
        if (next !== layer.get(fact.id)) {
          layer.set(fact.id, next)
          changed = true
        }
      }
      if (!changed) break
    }

    let goalLayer = 0
    for (const step of steps) {
      if (step.to_node_id !== goal.id) continue
      if (!factIds.has(step.from_node_id)) continue
      goalLayer = Math.max(goalLayer, (layer.get(step.from_node_id) ?? 0) + 1)
    }

    const columns = new Map<number, GraphNode[]>()
    const push = (column: number, node: GraphNode) => {
      const list = columns.get(column) ?? []
      list.push(node)
      columns.set(column, list)
    }
    for (const fact of facts) push(layer.get(fact.id) ?? 0, fact)
    push(goalLayer, goal)

    const placed: Placed[] = []
    for (const [column, columnNodes] of columns) {
      columnNodes.forEach((node, index) => {
        placed.push({ node, layer: column, index, count: columnNodes.length })
      })
    }

    const maxRows = Math.max(...Array.from(columns.values()).map((list) => list.length), 1)
    const contentHeight = maxRows * NODE_HEIGHT + (maxRows - 1) * ROW_GAP
    const rowPitch = NODE_HEIGHT + ROW_GAP

    // 每列相对最高列垂直居中，避免短列的节点悬在顶部。
    const positioned: Positioned[] = placed.map((item) => {
      const columnOffset = ((maxRows - item.count) * rowPitch) / 2
      return {
        ...item,
        x: PADDING + item.layer * (NODE_WIDTH + COLUMN_GAP),
        y: PADDING + columnOffset + item.index * rowPitch,
      }
    })

    const positionById = new Map(positioned.map((item) => [item.node.id, item]))
    const graphEdges = steps
      .map((step) => {
        const from = positionById.get(step.from_node_id)
        const to = positionById.get(step.to_node_id)
        if (!from || !to) return null
        return { id: step.id, from, to }
      })
      .filter((edge): edge is { id: number; from: Positioned; to: Positioned } => edge !== null)

    const maxLayer = Math.max(...Array.from(columns.keys()), 0)
    const totalWidth = PADDING * 2 + (maxLayer + 1) * NODE_WIDTH + maxLayer * COLUMN_GAP
    const totalHeight = PADDING * 2 + contentHeight

    return {
      nodes: positioned,
      width: Math.max(totalWidth, NODE_WIDTH + PADDING * 2),
      height: Math.max(totalHeight, NODE_HEIGHT + PADDING * 2),
      edges: graphEdges,
    }
  }, [facts, goal, steps])

  if (facts.length === 0 && steps.length === 0) {
    return null
  }

  return (
    <div className="overflow-x-auto rounded-md border border-border bg-canvas-subtle p-3">
      <svg
        aria-label="事实到目标的有向图"
        height={height}
        role="img"
        style={{ minWidth: width }}
        viewBox={`0 0 ${width} ${height}`}
        width={width}
      >
        <defs>
          <marker
            id="graph-arrow"
            markerHeight="7"
            markerWidth="7"
            orient="auto-start-reverse"
            refX="8"
            refY="3.5"
          >
            <path d="M0,0 L7,3.5 L0,7 Z" fill="var(--color-fg-muted)" />
          </marker>
        </defs>

        {edges.map((edge) => {
          const x1 = edge.from.x + NODE_WIDTH
          const y1 = edge.from.y + NODE_HEIGHT / 2
          const x2 = edge.to.x
          const y2 = edge.to.y + NODE_HEIGHT / 2
          const mid = (x1 + x2) / 2
          return (
            <path
              d={`M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2 - 4} ${y2}`}
              fill="none"
              key={edge.id}
              markerEnd="url(#graph-arrow)"
              stroke="var(--color-fg-muted)"
              strokeWidth={1.5}
            />
          )
        })}

        {nodes.map((item) => {
          const isGoal = item.node.kind === 'goal'
          return (
            <g key={item.node.id}>
              <rect
                fill={isGoal ? 'var(--color-canvas-subtle)' : 'var(--color-canvas)'}
                height={NODE_HEIGHT}
                rx={6}
                stroke={isGoal ? 'var(--color-success)' : 'var(--color-border)'}
                strokeWidth={1}
                width={NODE_WIDTH}
                x={item.x}
                y={item.y}
              />
              <text
                dominantBaseline="middle"
                fill={isGoal ? 'var(--color-success)' : 'var(--color-fg-muted)'}
                fontFamily="ui-monospace, SFMono-Regular, Menlo, monospace"
                fontSize={10}
                x={item.x + 10}
                y={item.y + 16}
              >
                {isGoal ? '目标' : `事实 #${item.node.id}`}
              </text>
              <foreignObject
                height={NODE_HEIGHT - 24}
                width={NODE_WIDTH - 20}
                x={item.x + 10}
                y={item.y + 24}
              >
                <div
                  style={{
                    color: 'var(--color-fg)',
                    fontSize: 12,
                    lineHeight: 1.3,
                    maxHeight: NODE_HEIGHT - 26,
                    overflow: 'hidden',
                  }}
                >
                  {item.node.content}
                </div>
              </foreignObject>
            </g>
          )
        })}
      </svg>
    </div>
  )
}
