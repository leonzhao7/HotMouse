import { useCallback, useEffect, useState } from 'react'

import { Alert } from '@/components/Alert'
import { Badge } from '@/components/Badge'
import { Button } from '@/components/Button'
import { Card } from '@/components/Card'
import { EmptyState } from '@/components/EmptyState'
import { PageLoading } from '@/components/Spinner'
import { ApiError, api } from '@/lib/api'
import { formatDateTime } from '@/lib/format'
import type { LLMLog } from '@/lib/types'

export function LLMLogsPage() {
  const [logs, setLogs] = useState<LLMLog[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      setLogs(await api.listLLMLogs())
    } catch (cause) {
      setLogs([])
      setError(cause instanceof ApiError ? cause.message : '加载 LLM 日志失败。')
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  if (logs === null) {
    return <PageLoading />
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold text-fg md:text-3xl">LLM 日志</h1>
          <p className="mt-1 text-sm text-fg-muted">最近保存的 Dispatch LLM 请求和响应记录。</p>
        </div>
        <Button onClick={() => void load()} size="sm">
          刷新
        </Button>
      </div>

      {error ? <Alert tone="danger">{error}</Alert> : null}

      {logs.length === 0 ? (
        <EmptyState
          description="Dispatch 开始调用 LLM 后，完整的请求和响应会显示在这里。"
          title="暂无 LLM 日志"
        />
      ) : (
        <Card className="overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full min-w-[960px] border-collapse text-left text-sm">
              <thead className="bg-canvas-subtle text-xs text-fg-muted">
                <tr>
                  <th className="whitespace-nowrap border-b border-border px-3 py-2 font-semibold">时间</th>
                  <th className="whitespace-nowrap border-b border-border px-3 py-2 font-semibold">类型</th>
                  <th className="border-b border-border px-3 py-2 font-semibold">Model / URL</th>
                  <th className="whitespace-nowrap border-b border-border px-3 py-2 font-semibold">Token</th>
                  <th className="border-b border-border px-3 py-2 font-semibold">请求</th>
                  <th className="border-b border-border px-3 py-2 font-semibold">响应</th>
                </tr>
              </thead>
              <tbody>
                {logs.map((log) => (
                  <tr className="align-top" key={log.id}>
                    <td className="whitespace-nowrap border-b border-border px-3 py-3 text-xs text-fg-muted">
                      {formatDateTime(log.started_at)}
                    </td>
                    <td className="border-b border-border px-3 py-3">
                      <Badge tone={log.error ? 'danger' : 'accent'}>
                        {log.operation === 'summary' ? '总结' : '计划'}
                      </Badge>
                      <p className="mt-1 text-xs text-fg-muted">
                        {log.status_code > 0 ? `HTTP ${log.status_code}` : '请求失败'}
                      </p>
                    </td>
                    <td className="max-w-64 border-b border-border px-3 py-3">
                      <p className="font-medium text-fg">{log.model}</p>
                      <p className="mt-1 break-all text-xs text-fg-muted">{log.url}</p>
                    </td>
                    <td className="whitespace-nowrap border-b border-border px-3 py-3 text-xs text-fg-muted">
                      <p>上限 {log.max_tokens}</p>
                      <p>输入 {log.prompt_tokens}</p>
                      <p>输出 {log.completion_tokens}</p>
                      <p>合计 {log.total_tokens}</p>
                    </td>
                    <td className="max-w-80 border-b border-border px-3 py-3">
                      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-canvas-subtle p-2 font-mono text-xs text-fg">
                        {log.prompt}
                      </pre>
                    </td>
                    <td className="max-w-80 border-b border-border px-3 py-3">
                      <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-canvas-subtle p-2 font-mono text-xs text-fg">
                        {log.response || '（空响应）'}
                      </pre>
                      {log.error ? <p className="mt-2 break-words text-xs text-danger">错误：{log.error}</p> : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  )
}
