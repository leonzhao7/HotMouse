import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'

import { Alert } from '@/components/Alert'
import { Button } from '@/components/Button'
import { Card } from '@/components/Card'
import { TextArea, TextInput } from '@/components/Field'
import { IconEye, IconEyeClosed } from '@/components/icons'
import { PageLoading } from '@/components/Spinner'
import { ApiError, api } from '@/lib/api'
import { formatDateTime } from '@/lib/format'

const MAX_TOKENS_LIMIT = 10_000_000
const MAX_ITERATIONS_LIMIT = 100

interface ProfileValues {
  url: string
  apiKey: string
  model: string
  maxTokens: string
  command: string
}

const emptyProfile: ProfileValues = { url: '', apiKey: '', model: '', maxTokens: '', command: '' }

function ProfileFields({
  label,
  profile,
  onChange,
}: {
  label: string
  profile: ProfileValues
  onChange: (next: ProfileValues) => void
}) {
  const [showApiKey, setShowApiKey] = useState(false)

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h2 className="text-base font-semibold text-fg">{label} LLM</h2>
        <p className="mt-1 text-xs text-fg-muted">
          {label === 'Dispatch'
            ? '地址、模型或 Token 数留空时沿用 Worker 配置；API Key 留空表示不鉴权。'
            : 'Worker 配置用于执行任务；API Key 可留空以连接无需鉴权的本地模型。'}
        </p>
      </div>
      <TextInput
        hint="OpenAI 兼容接口的 base URL，例如 https://api.openai.com/v1。"
        label="LLM 地址"
        onChange={(event) => onChange({ ...profile, url: event.target.value })}
        placeholder="https://api.openai.com/v1"
        value={profile.url}
      />
      <TextInput
        autoComplete="off"
        hint="可留空，用于无需鉴权的本地模型。"
        label="API Key"
        onChange={(event) => onChange({ ...profile, apiKey: event.target.value })}
        spellCheck={false}
        trailingAction={
          <button
            aria-label={showApiKey ? '隐藏 API Key' : '显示 API Key'}
            aria-pressed={showApiKey}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md text-fg-muted transition-colors duration-150 hover:bg-canvas-subtle hover:text-fg focus:outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
            onClick={() => setShowApiKey((visible) => !visible)}
            title={showApiKey ? '隐藏 API Key' : '显示 API Key'}
            type="button"
          >
            {showApiKey ? <IconEyeClosed /> : <IconEye />}
          </button>
        }
        type={showApiKey ? 'text' : 'password'}
        value={profile.apiKey}
      />
      <TextInput
        hint="传给接口的模型标识，例如 gpt-4o-mini。"
        label="模型"
        onChange={(event) => onChange({ ...profile, model: event.target.value })}
        placeholder="gpt-4o-mini"
        value={profile.model}
      />
      <TextInput
        hint={`单次调用允许生成的最大 token 数，取值 1 到 ${MAX_TOKENS_LIMIT}。`}
        inputMode="numeric"
        label="最大 Token 数"
        max={MAX_TOKENS_LIMIT}
        min={1}
        onChange={(event) => onChange({ ...profile, maxTokens: event.target.value })}
        placeholder="1024"
        step={1}
        type="number"
        value={profile.maxTokens}
      />
      {label === 'Worker' ? (
        <TextArea
          hint="可填写外部 Worker 命令。不经过 Shell 执行；支持 {{prompt}} 占位符注入参数，同时也会将 prompt 写入标准输入。"
          label="Worker 启动命令行"
          onChange={(event) => onChange({ ...profile, command: event.target.value })}
          placeholder="my-worker --prompt {{prompt}}"
          spellCheck={false}
          value={profile.command}
        />
      ) : null}
    </div>
  )
}

export function SettingsPage() {
  const [worker, setWorker] = useState<ProfileValues>(emptyProfile)
  const [dispatch, setDispatch] = useState<ProfileValues>(emptyProfile)
  const [maxIterations, setMaxIterations] = useState('')
  const [promptPlan, setPromptPlan] = useState('')
  const [promptWorker, setPromptWorker] = useState('')
  const [promptSummarize, setPromptSummarize] = useState('')
  const [updatedAt, setUpdatedAt] = useState<string | null>(null)

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const settings = await api.getSettings()
        if (cancelled) return
        setWorker({
          url: settings.worker_llm_url || settings.llm_url,
          apiKey: settings.worker_api_key || settings.api_key,
          model: settings.worker_model || settings.model,
          maxTokens: String(settings.worker_max_tokens || settings.max_tokens || ''),
          command: settings.worker_command,
        })
        setDispatch({
          url: settings.dispatch_llm_url,
          apiKey: settings.dispatch_api_key,
          model: settings.dispatch_model,
          maxTokens: String(settings.dispatch_max_tokens || ''),
          command: '',
        })
        setMaxIterations(String(settings.max_iterations || ''))
        setPromptPlan(settings.dispatch_plan_prompt)
        setPromptWorker(settings.worker_prompt)
        setPromptSummarize(settings.dispatch_summary_prompt)
        setUpdatedAt(settings.updated_at ?? null)
      } catch (cause) {
        if (!cancelled) {
          setError(cause instanceof ApiError ? cause.message : '加载设置失败。')
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [])

  const handleSubmit = async (event: FormEvent) => {
    event.preventDefault()
    setError(null)
    setNotice(null)

    const workerUrl = worker.url.trim()
    const workerModel = worker.model.trim()
    const workerTokens = Number(worker.maxTokens)
    if (!workerUrl || !workerModel) {
      setError('Worker 的 LLM 地址和模型都是必填项。')
      return
    }
    if (!Number.isInteger(workerTokens) || workerTokens <= 0 || workerTokens > MAX_TOKENS_LIMIT) {
      setError(`Worker 最大 Token 数必须是 1 到 ${MAX_TOKENS_LIMIT} 之间的整数。`)
      return
    }
    const dispatchTokens = dispatch.maxTokens.trim() ? Number(dispatch.maxTokens) : workerTokens
    if (!Number.isInteger(dispatchTokens) || dispatchTokens <= 0 || dispatchTokens > MAX_TOKENS_LIMIT) {
      setError(`Dispatch 最大 Token 数必须是 1 到 ${MAX_TOKENS_LIMIT} 之间的整数。`)
      return
    }
    const iterations = Number(maxIterations)
    if (!Number.isInteger(iterations) || iterations < 1 || iterations > MAX_ITERATIONS_LIMIT) {
      setError(`Dispatch 最大迭代次数必须是 1 到 ${MAX_ITERATIONS_LIMIT} 之间的整数。`)
      return
    }
    if (!promptPlan.trim() || !promptWorker.trim() || !promptSummarize.trim()) {
      setError('三个提示词都不能为空。')
      return
    }

    setSaving(true)
    try {
      const saved = await api.updateSettings({
        llm_url: workerUrl,
        api_key: worker.apiKey.trim(),
        model: workerModel,
        max_tokens: workerTokens,
        worker_llm_url: workerUrl,
        worker_api_key: worker.apiKey.trim(),
        worker_model: workerModel,
        worker_max_tokens: workerTokens,
        worker_command: worker.command.trim(),
        dispatch_llm_url: dispatch.url.trim(),
        dispatch_api_key: dispatch.apiKey.trim(),
        dispatch_model: dispatch.model.trim(),
        dispatch_max_tokens: dispatchTokens,
        max_iterations: iterations,
        worker_prompt: promptWorker,
        dispatch_plan_prompt: promptPlan,
        dispatch_summary_prompt: promptSummarize,
      })
      setWorker({
        url: saved.worker_llm_url || saved.llm_url,
        apiKey: saved.worker_api_key || saved.api_key,
        model: saved.worker_model || saved.model,
        maxTokens: String(saved.worker_max_tokens || saved.max_tokens),
        command: saved.worker_command,
      })
      setDispatch({
        url: saved.dispatch_llm_url,
        apiKey: saved.dispatch_api_key,
        model: saved.dispatch_model,
        maxTokens: String(saved.dispatch_max_tokens),
        command: '',
      })
      setMaxIterations(String(saved.max_iterations))
      setPromptPlan(saved.dispatch_plan_prompt)
      setPromptWorker(saved.worker_prompt)
      setPromptSummarize(saved.dispatch_summary_prompt)
      setUpdatedAt(saved.updated_at ?? null)
      setNotice('设置已保存。')
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : '保存设置失败。')
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return <PageLoading />
  }

  return (
    <div className="flex max-w-4xl flex-col gap-4">
      <div>
        <h1 className="text-2xl font-semibold text-fg md:text-3xl">设置</h1>
        <p className="mt-1 text-sm text-fg-muted">
          配置 worker、dispatch 使用的模型，以及程序执行过程中使用的提示词。保存后写入本地数据库。
        </p>
      </div>

      {error ? <Alert tone="danger">{error}</Alert> : null}
      {notice ? <Alert tone="success">{notice}</Alert> : null}

      <form onSubmit={handleSubmit}>
        <div className="flex flex-col gap-4">
          <Card className="grid gap-6 p-4 md:grid-cols-2">
            <ProfileFields label="Worker" onChange={setWorker} profile={worker} />
            <ProfileFields label="Dispatch" onChange={setDispatch} profile={dispatch} />
          </Card>

          <Card className="flex flex-col gap-4 p-4">
            <div>
              <h2 className="text-base font-semibold text-fg">Dispatch 行为</h2>
              <p className="mt-1 text-xs text-fg-muted">
                提示词支持双大括号变量，保存前请保留需要注入上下文的位置。
              </p>
            </div>
            <TextInput
              hint={`每个项目最多连续处理 ${MAX_ITERATIONS_LIMIT} 次，建议保持较小值。`}
              inputMode="numeric"
              label="最大迭代次数"
              max={MAX_ITERATIONS_LIMIT}
              min={1}
              onChange={(event) => setMaxIterations(event.target.value)}
              step={1}
              type="number"
              value={maxIterations}
            />
          </Card>

          <Card className="flex flex-col gap-4 p-4">
            <div>
              <h2 className="text-base font-semibold text-fg">提示词模板</h2>
              <p className="mt-1 text-xs text-fg-muted">
                可用变量：计划提示词 `{'{{graph_json}}'}`；worker 提示词 `{'{{goal}}'}`、`{'{{fact}}'}`、`{'{{fact_id}}'}`、`{'{{action}}'}`；总结提示词 `{'{{graph_json}}'}`、`{'{{chosen_action_json}}'}`、`{'{{worker_output}}'}`。
              </p>
            </div>
            <TextArea
              className="min-h-48 font-mono"
              label="Dispatch 计划提示词"
              onChange={(event) => setPromptPlan(event.target.value)}
              spellCheck={false}
              value={promptPlan}
            />
            <TextArea
              className="min-h-48 font-mono"
              label="Worker 执行提示词"
              onChange={(event) => setPromptWorker(event.target.value)}
              spellCheck={false}
              value={promptWorker}
            />
            <TextArea
              className="min-h-48 font-mono"
              label="Dispatch 总结提示词"
              onChange={(event) => setPromptSummarize(event.target.value)}
              spellCheck={false}
              value={promptSummarize}
            />
          </Card>

          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={saving} type="submit" variant="primary">
              {saving ? '保存中…' : '保存设置'}
            </Button>
            {updatedAt ? (
              <span className="text-xs text-fg-subtle">上次保存：{formatDateTime(updatedAt)}</span>
            ) : null}
          </div>
        </div>
      </form>
    </div>
  )
}
