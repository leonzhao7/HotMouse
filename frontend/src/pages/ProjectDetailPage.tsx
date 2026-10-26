import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router-dom'

import { Alert } from '@/components/Alert'
import { Badge } from '@/components/Badge'
import { Button } from '@/components/Button'
import { Card } from '@/components/Card'
import { TextArea } from '@/components/Field'
import { GraphView } from '@/components/GraphView'
import { IconArrowRight, IconCheck, IconLink, IconLock, IconPlus, IconTarget } from '@/components/icons'
import { PageLoading } from '@/components/Spinner'
import { ApiError, api } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import type { GraphNode, Project, Step } from '@/lib/types'

interface DetailData {
  project: Project
  goal: GraphNode
  facts: GraphNode[]
  steps: Step[]
}

export function ProjectDetailPage() {
  const params = useParams<{ id: string }>()
  const projectId = Number(params.id)

  const [data, setData] = useState<DetailData | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const [factContent, setFactContent] = useState('')
  const [fromNodeId, setFromNodeId] = useState<number | ''>('')
  const [toNodeId, setToNodeId] = useState<number | ''>('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    if (!Number.isInteger(projectId) || projectId <= 0) {
      setLoadError('项目 ID 无效。')
      return
    }
    setLoadError(null)
    try {
      const [projects, goal, facts, steps] = await Promise.all([
        api.listProjects(),
        api.getGoal(projectId),
        api.listFacts(projectId),
        api.listSteps(projectId),
      ])
      const project = projects.find((item) => item.id === projectId)
      if (!project) {
        setLoadError('项目不存在或已被移除。')
        return
      }
      setData({ project, goal, facts, steps })
    } catch (cause) {
      setLoadError(cause instanceof ApiError ? cause.message : '加载项目失败。')
    }
  }, [projectId])

  useEffect(() => {
    void load()
  }, [load])

  const isCompleted = data?.project.status === 'completed'

  const factById = useMemo(() => {
    const map = new Map<number, GraphNode>()
    for (const fact of data?.facts ?? []) map.set(fact.id, fact)
    return map
  }, [data?.facts])

  const goalIncoming = useMemo(
    () => (data?.steps ?? []).filter((step) => step.to_node_id === data?.goal.id),
    [data?.steps, data?.goal.id],
  )

  const runAction = async (action: () => Promise<string>) => {
    setBusy(true)
    setActionError(null)
    setNotice(null)
    try {
      const message = await action()
      setNotice(message)
      await load()
    } catch (cause) {
      setActionError(cause instanceof ApiError ? cause.message : '操作失败。')
    } finally {
      setBusy(false)
    }
  }

  const handleAddFact = (event: React.FormEvent) => {
    event.preventDefault()
    const content = factContent.trim()
    if (!content) {
      setActionError('事实内容不能为空。')
      return
    }
    void runAction(async () => {
      await api.addFact(projectId, content)
      setFactContent('')
      return '已追加事实。'
    })
  }

  const handleAddStep = (event: React.FormEvent) => {
    event.preventDefault()
    if (fromNodeId === '' || toNodeId === '') {
      setActionError('请选择起点和终点。')
      return
    }
    void runAction(async () => {
      await api.addStep(projectId, {
        from_node_id: Number(fromNodeId),
        to_node_id: Number(toNodeId),
      })
      setFromNodeId('')
      setToNodeId('')
      return '已追加步骤。'
    })
  }

  const handleComplete = () => {
    void runAction(async () => {
      await api.completeProject(projectId)
      return '项目已完成。'
    })
  }

  if (loadError) {
    return (
      <div className="flex flex-col gap-3">
        <Alert tone="danger">{loadError}</Alert>
        <Link to="/">
          <Button>返回项目列表</Button>
        </Link>
      </div>
    )
  }

  if (!data) {
    return <PageLoading />
  }

  const { project, goal, facts, steps } = data
  const selectClass =
    'rounded-md border border-border bg-canvas-subtle px-2 py-1.5 text-sm text-nav focus:bg-canvas focus:outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent'

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold text-fg md:text-3xl">{project.name}</h1>
            <Badge tone={isCompleted ? 'success' : 'accent'}>
              {isCompleted ? '已完成' : '进行中'}
            </Badge>
          </div>
          {project.description ? (
            <p className="mt-1 text-sm text-fg-muted">{project.description}</p>
          ) : null}
          <p className="mt-1 text-xs text-fg-subtle">
            #{project.id} · 创建于 {formatDateTime(project.created_at)}
            {project.completed_at
              ? ` · 完成于 ${formatDateTime(project.completed_at)}`
              : null}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Link to="/">
            <Button>返回列表</Button>
          </Link>
          {isCompleted ? (
            <Button disabled variant="secondary">
              <IconLock className="h-4 w-4" />
              已完成
            </Button>
          ) : (
            <Button
              disabled={busy || goalIncoming.length === 0}
              onClick={handleComplete}
              variant="primary"
            >
              <IconCheck className="h-4 w-4" />
              标记为完成
            </Button>
          )}
        </div>
      </div>

      {actionError ? <Alert tone="danger">{actionError}</Alert> : null}
      {notice ? <Alert tone="success">{notice}</Alert> : null}
      {!isCompleted && goalIncoming.length === 0 ? (
        <Alert tone="info">
          目标目前不可达 —— 至少需要一条指向目标的步骤才能完成项目。
        </Alert>
      ) : null}

      <Card className="p-4">
        <div className="flex items-center gap-2">
          <IconTarget className="h-4 w-4 text-success" />
          <p className="text-sm font-semibold text-fg">目标</p>
        </div>
        <p className="mt-2 text-sm text-fg">{goal.content}</p>
        <p className="mt-1 text-xs text-fg-subtle">
          节点 #{goal.id} · 创建于 {formatDateTime(goal.created_at)}
        </p>
      </Card>

      <section className="flex flex-col gap-3">
        <h2 className="text-xl font-semibold text-fg md:text-2xl">图谱</h2>
        <GraphView facts={facts} goal={goal} steps={steps} />
      </section>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card className="flex flex-col gap-3 p-4">
          <div className="flex items-center justify-between">
            <h2 className="text-lg font-semibold text-fg">事实</h2>
            <span className="text-xs text-fg-muted">{facts.length} 条</span>
          </div>

          <ul className="flex flex-col gap-2">
            {facts.map((fact) => (
              <li
                className="rounded-md border border-border bg-canvas-subtle p-3"
                key={fact.id}
              >
                <p className="text-sm text-fg">{fact.content}</p>
                <p className="mt-1 text-xs text-fg-subtle">
                  #{fact.id} · {formatDateTime(fact.created_at)}
                </p>
              </li>
            ))}
            {facts.length === 0 ? (
              <li className="rounded-md border border-dashed border-border p-3 text-sm text-fg-muted">
                还没有事实，先追加一条再连接步骤。
              </li>
            ) : null}
          </ul>

          {isCompleted ? null : (
            <form className="flex flex-col gap-2 border-t border-border pt-3" onSubmit={handleAddFact}>
              <TextArea
                label="追加事实"
                onChange={(event) => setFactContent(event.target.value)}
                placeholder="描述一件新的已知事实"
                rows={2}
                value={factContent}
              />
              <Button className="self-end" disabled={busy} type="submit" variant="primary">
                <IconPlus className="h-4 w-4" />
                追加事实
              </Button>
            </form>
          )}
        </Card>

        <Card className="flex flex-col gap-3 p-4">
          <div className="flex items-center justify-between">
            <h2 className="text-lg font-semibold text-fg">步骤</h2>
            <span className="text-xs text-fg-muted">{steps.length} 条</span>
          </div>

          <ul className="flex flex-col gap-2">
            {steps.map((step) => {
              const from = factById.get(step.from_node_id)
              const to = step.to_node_id === goal.id ? goal : factById.get(step.to_node_id)
              return (
                <li
                  className="flex items-start gap-2 rounded-md border border-border bg-canvas-subtle p-3"
                  key={step.id}
                >
                  <IconLink className="mt-0.5 h-4 w-4 shrink-0 text-fg-subtle" />
                  <div className="min-w-0 flex-1">
                    <p className="flex flex-wrap items-center gap-1 text-sm text-fg">
                      <span className="font-mono text-xs text-fg-muted">
                        #{step.from_node_id}
                      </span>
                      {from?.content ?? '未知节点'}
                      <IconArrowRight className="h-3.5 w-3.5 text-fg-subtle" />
                      <span className="font-mono text-xs text-fg-muted">
                        #{step.to_node_id}
                      </span>
                      {to?.content ?? '未知节点'}
                    </p>
                    <p className="mt-1 text-xs text-fg-subtle">
                      步骤 #{step.id} · {formatDateTime(step.created_at)}
                    </p>
                  </div>
                </li>
              )
            })}
            {steps.length === 0 ? (
              <li className="rounded-md border border-dashed border-border p-3 text-sm text-fg-muted">
                还没有步骤。步骤只能从事实出发，终点可以是事实或目标。
              </li>
            ) : null}
          </ul>

          {isCompleted ? null : (
            <form
              className="flex flex-col gap-2 border-t border-border pt-3"
              onSubmit={handleAddStep}
            >
              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <label className="text-xs font-semibold text-fg" htmlFor="from-node">
                    起点（事实）
                  </label>
                  <select
                    className={selectClass}
                    id="from-node"
                    onChange={(event) =>
                      setFromNodeId(event.target.value === '' ? '' : Number(event.target.value))
                    }
                    value={fromNodeId}
                  >
                    <option value="">选择事实</option>
                    {facts.map((fact) => (
                      <option key={fact.id} value={fact.id}>
                        #{fact.id} {fact.content.slice(0, 24)}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="flex flex-col gap-1">
                  <label className="text-xs font-semibold text-fg" htmlFor="to-node">
                    终点（事实或目标）
                  </label>
                  <select
                    className={selectClass}
                    id="to-node"
                    onChange={(event) =>
                      setToNodeId(event.target.value === '' ? '' : Number(event.target.value))
                    }
                    value={toNodeId}
                  >
                    <option value="">选择节点</option>
                    {facts.map((fact) => (
                      <option key={fact.id} value={fact.id}>
                        #{fact.id} {fact.content.slice(0, 24)}
                      </option>
                    ))}
                    <option value={goal.id}>#{goal.id} 目标</option>
                  </select>
                </div>
              </div>
              <Button className="self-end" disabled={busy} type="submit" variant="primary">
                <IconPlus className="h-4 w-4" />
                追加步骤
              </Button>
            </form>
          )}
        </Card>
      </div>
    </div>
  )
}
