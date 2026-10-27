import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'

import { Alert } from '@/components/Alert'
import { Button } from '@/components/Button'
import { Card } from '@/components/Card'
import { TextInput } from '@/components/Field'
import { PageLoading } from '@/components/Spinner'
import { ApiError, api } from '@/lib/api'
import { formatDateTime } from '@/lib/format'

const MAX_TOKENS_LIMIT = 10_000_000

export function SettingsPage() {
  const [llmUrl, setLlmUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [model, setModel] = useState('')
  const [maxTokens, setMaxTokens] = useState('')
  const [updatedAt, setUpdatedAt] = useState<string | null>(null)
  const [showKey, setShowKey] = useState(false)

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
        setLlmUrl(settings.llm_url)
        setApiKey(settings.api_key)
        setModel(settings.model)
        setMaxTokens(settings.max_tokens > 0 ? String(settings.max_tokens) : '')
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

    const url = llmUrl.trim()
    const name = model.trim()
    const tokens = Number(maxTokens)
    if (!url || !name) {
      setError('LLM 地址和模型都是必填项。')
      return
    }
    if (!Number.isInteger(tokens) || tokens <= 0 || tokens > MAX_TOKENS_LIMIT) {
      setError(`最大 Token 数必须是 1 到 ${MAX_TOKENS_LIMIT} 之间的整数。`)
      return
    }

    setSaving(true)
    try {
      const saved = await api.updateSettings({
        llm_url: url,
        api_key: apiKey.trim(),
        model: name,
        max_tokens: tokens,
      })
      setLlmUrl(saved.llm_url)
      setApiKey(saved.api_key)
      setModel(saved.model)
      setMaxTokens(String(saved.max_tokens))
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
    <div className="flex max-w-3xl flex-col gap-4">
      <div>
        <h1 className="text-2xl font-semibold text-fg md:text-3xl">设置</h1>
        <p className="mt-1 text-sm text-fg-muted">
          配置后端调用 LLM 时使用的地址、密钥、模型和最大 token 数，保存后写入本地数据库。
        </p>
      </div>

      {error ? <Alert tone="danger">{error}</Alert> : null}
      {notice ? <Alert tone="success">{notice}</Alert> : null}

      <form onSubmit={handleSubmit}>
        <Card className="flex flex-col gap-4 p-4">
          <TextInput
            hint="OpenAI 兼容接口的 base URL，例如 https://api.openai.com/v1。"
            label="LLM 地址"
            onChange={(event) => setLlmUrl(event.target.value)}
            placeholder="https://api.openai.com/v1"
            value={llmUrl}
          />

          <div className="flex items-start gap-2">
            <div className="flex-1">
              <TextInput
                autoComplete="off"
                hint="留空可用于无需鉴权的本地模型；页面会原样显示已保存的密钥。"
                label="API Key"
                onChange={(event) => setApiKey(event.target.value)}
                spellCheck={false}
                type={showKey ? 'text' : 'password'}
                value={apiKey}
              />
            </div>
            <Button
              aria-pressed={showKey}
              className="mt-6"
              onClick={() => setShowKey((current) => !current)}
              size="sm"
            >
              {showKey ? '隐藏' : '显示'}
            </Button>
          </div>

          <TextInput
            hint="传给接口的模型标识，例如 gpt-4o-mini。"
            label="模型"
            onChange={(event) => setModel(event.target.value)}
            placeholder="gpt-4o-mini"
            value={model}
          />

          <TextInput
            hint={`单次调用允许生成的最大 token 数，取值 1 到 ${MAX_TOKENS_LIMIT}。`}
            inputMode="numeric"
            label="最大 Token 数"
            max={MAX_TOKENS_LIMIT}
            min={1}
            onChange={(event) => setMaxTokens(event.target.value)}
            placeholder="1024"
            step={1}
            type="number"
            value={maxTokens}
          />

          <div className="flex flex-wrap items-center gap-2 border-t border-border pt-3">
            <Button disabled={saving} type="submit" variant="primary">
              {saving ? '保存中…' : '保存设置'}
            </Button>
            {updatedAt ? (
              <span className="text-xs text-fg-subtle">
                上次保存：{formatDateTime(updatedAt)}
              </span>
            ) : null}
          </div>
        </Card>
      </form>
    </div>
  )
}
