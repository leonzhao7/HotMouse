import type {
  AddStepInput,
  ApiErrorBody,
  ApiErrorCode,
  CreateProjectInput,
  Graph,
  GraphNode,
  Project,
  Settings,
  Step,
  UpdateSettingsInput,
} from './types'

/** 后端返回的结构化错误。 */
export class ApiError extends Error {
  readonly code: ApiErrorCode | 'network_error'
  readonly status: number

  constructor(code: ApiErrorCode | 'network_error', message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
  }
}

const BASE = '/api'

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let response: Response
  try {
    response = await fetch(`${BASE}${path}`, {
      ...init,
      headers: {
        Accept: 'application/json',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...init?.headers,
      },
    })
  } catch {
    throw new ApiError('network_error', '无法连接到后端服务，请确认它已启动。', 0)
  }

  if (response.status === 204) {
    return undefined as T
  }

  const text = await response.text()
  let payload: unknown = null
  if (text.length > 0) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = null
    }
  }

  if (!response.ok) {
    const body = payload as ApiErrorBody | null
    throw new ApiError(
      body?.error?.code ?? 'internal_error',
      body?.error?.message ?? `请求失败 (HTTP ${response.status})`,
      response.status,
    )
  }

  return payload as T
}

export const api = {
  listProjects: () => request<Project[]>('/project'),

  createProject: (input: CreateProjectInput) =>
    request<Graph>('/project', { method: 'POST', body: JSON.stringify(input) }),

  getGoal: (projectId: number) => request<GraphNode>(`/project/${projectId}/goal`),

  completeProject: (projectId: number) =>
    request<Project>(`/project/${projectId}/goal`, {
      method: 'POST',
      body: JSON.stringify({}),
    }),

  listFacts: (projectId: number) => request<GraphNode[]>(`/project/${projectId}/fact`),

  addFact: (projectId: number, content: string) =>
    request<GraphNode>(`/project/${projectId}/fact`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    }),

  listSteps: (projectId: number) => request<Step[]>(`/project/${projectId}/step`),

  addStep: (projectId: number, input: AddStepInput) =>
    request<Step>(`/project/${projectId}/step`, {
      method: 'POST',
      body: JSON.stringify(input),
    }),

  getSettings: () => request<Settings>('/settings'),

  updateSettings: (input: UpdateSettingsInput) =>
    request<Settings>('/settings', {
      method: 'PUT',
      body: JSON.stringify(input),
    }),
}
