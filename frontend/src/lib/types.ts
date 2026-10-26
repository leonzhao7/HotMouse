/** 与 docs/API.md 一一对应的类型定义。 */

export type ProjectStatus = 'open' | 'completed'

export type NodeKind = 'fact' | 'goal'

export interface Project {
  id: number
  name: string
  description?: string
  status: ProjectStatus
  created_at: string
  completed_at?: string
}

export interface GraphNode {
  id: number
  project_id: number
  kind: NodeKind
  content: string
  created_at: string
}

export interface Step {
  id: number
  project_id: number
  from_node_id: number
  to_node_id: number
  created_at: string
}

/** POST /project 的响应体 (domain.Graph)。 */
export interface Graph {
  id: number
  name: string
  description?: string
  status: ProjectStatus
  created_at: string
  completed_at?: string
  goal: GraphNode
  facts: GraphNode[]
  steps: Step[]
}

export interface CreateProjectInput {
  name: string
  description?: string
  goal: string
  facts: string[]
}

export interface AddStepInput {
  from_node_id: number
  to_node_id: number
}

/** 后端错误码, 见 docs/API.md 的 "Validation and errors"。 */
export type ApiErrorCode =
  | 'invalid_graph'
  | 'not_found'
  | 'duplicate_step'
  | 'goal_unreachable'
  | 'project_completed'
  | 'invalid_json'
  | 'invalid_project_id'
  | 'unsupported_media_type'
  | 'method_not_allowed'
  | 'body_too_large'
  | 'internal_error'

export interface ApiErrorBody {
  error: {
    code: ApiErrorCode
    message: string
  }
}
