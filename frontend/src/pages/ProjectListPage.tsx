import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'

import { Alert } from '@/components/Alert'
import { Badge } from '@/components/Badge'
import { Button } from '@/components/Button'
import { Card } from '@/components/Card'
import { EmptyState } from '@/components/EmptyState'
import { IconPlus, IconRepo, IconSearch } from '@/components/icons'
import { PageLoading } from '@/components/Spinner'
import { ApiError, api } from '@/lib/api'
import { formatRelative } from '@/lib/format'
import type { Project } from '@/lib/types'

export function ProjectListPage() {
  const [projects, setProjects] = useState<Project[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'open' | 'completed'>('all')

  const load = useCallback(async () => {
    setError(null)
    try {
      setProjects(await api.listProjects())
    } catch (cause) {
      setProjects([])
      setError(cause instanceof ApiError ? cause.message : '加载项目列表失败。')
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const filtered = useMemo(() => {
    if (!projects) return []
    const keyword = query.trim().toLowerCase()
    return projects.filter((project) => {
      if (statusFilter !== 'all' && project.status !== statusFilter) return false
      if (!keyword) return true
      return (
        project.name.toLowerCase().includes(keyword) ||
        (project.description ?? '').toLowerCase().includes(keyword)
      )
    })
  }, [projects, query, statusFilter])

  if (projects === null) {
    return <PageLoading />
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-fg md:text-3xl">项目</h1>
          <p className="mt-1 text-sm text-fg-muted">
            共 {projects.length} 个项目，按目标推进的事实图谱。
          </p>
        </div>
        <Link to="/new">
          <Button variant="primary">
            <IconPlus className="h-4 w-4" />
            新建项目
          </Button>
        </Link>
      </div>

      {error ? <Alert tone="danger">{error}</Alert> : null}

      <Card className="flex flex-wrap items-center gap-3 p-3">
        <div className="relative min-w-56 flex-1">
          <IconSearch className="pointer-events-none absolute left-2 top-1/2 h-4 w-4 -translate-y-1/2 text-fg-subtle" />
          <input
            aria-label="搜索项目"
            className="w-full rounded-md border border-border bg-canvas-subtle py-1.5 pl-8 pr-3 text-sm text-nav placeholder:text-fg-subtle focus:bg-canvas focus:outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
            onChange={(event) => setQuery(event.target.value)}
            placeholder="按名称或描述搜索"
            type="search"
            value={query}
          />
        </div>
        <div className="flex items-center gap-1">
          {(
            [
              ['all', '全部'],
              ['open', '进行中'],
              ['completed', '已完成'],
            ] as const
          ).map(([value, label]) => (
            <Button
              className={statusFilter === value ? 'border-accent text-accent' : ''}
              key={value}
              onClick={() => setStatusFilter(value)}
              size="sm"
            >
              {label}
            </Button>
          ))}
        </div>
      </Card>

      {filtered.length === 0 ? (
        <EmptyState
          action={
            <Link to="/new">
              <Button variant="primary">
                <IconPlus className="h-4 w-4" />
                新建项目
              </Button>
            </Link>
          }
          description="每个项目包含一个目标和若干事实，事实之间通过有向步骤连接。"
          icon={<IconRepo className="h-8 w-8" />}
          title={projects.length === 0 ? '还没有项目' : '没有匹配的项目'}
        />
      ) : (
        <div className="flex flex-col gap-3">
          {filtered.map((project) => (
            <Card className="p-4 transition-colors duration-150 hover:border-accent/40" key={project.id}>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <Link
                    className="text-base font-semibold text-accent hover:underline"
                    to={`/projects/${project.id}`}
                  >
                    {project.name}
                  </Link>
                  {project.description ? (
                    <p className="mt-1 text-sm text-fg-muted">{project.description}</p>
                  ) : null}
                </div>
                <Badge tone={project.status === 'completed' ? 'success' : 'accent'}>
                  {project.status === 'completed' ? '已完成' : '进行中'}
                </Badge>
              </div>
              <p className="mt-3 text-xs text-fg-subtle">
                #{project.id} · 创建于 {formatRelative(project.created_at)}
                {project.completed_at
                  ? ` · 完成于 ${formatRelative(project.completed_at)}`
                  : null}
              </p>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}
