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
  description?: string
  created_at: string
}

/** POST /project 和 GET /project/{id}/export 的图谱结构。 */
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
  description?: string
}

/** 全局单例的 LLM 配置 (GET/PUT /settings)。 */
export interface Settings {
  llm_url: string
  api_key: string
  model: string
  max_tokens: number
  updated_at?: string
}

export interface UpdateSettingsInput {
  llm_url: string
  api_key: string
  model: string
  max_tokens: number
}

/** 后端错误码, 见 docs/API.md 的 "Validation and errors"。 */
export type ApiErrorCode =
  | 'invalid_graph'
  | 'invalid_settings'
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
