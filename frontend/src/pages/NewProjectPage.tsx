import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'

import { Alert } from '@/components/Alert'
import { Button } from '@/components/Button'
import { Card } from '@/components/Card'
import { TextArea, TextInput } from '@/components/Field'
import { IconPlus, IconTrash } from '@/components/icons'
import { ApiError, api } from '@/lib/api'

export function NewProjectPage() {
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [goal, setGoal] = useState('')
  const [facts, setFacts] = useState<string[]>([''])
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const updateFact = (index: number, value: string) => {
    setFacts((current) => current.map((fact, i) => (i === index ? value : fact)))
  }

  const addFact = () => {
    setFacts((current) => [...current, ''])
  }

  const removeFact = (index: number) => {
    setFacts((current) => current.filter((_, i) => i !== index))
  }

  const handleSubmit = async (event: React.FormEvent) => {
    event.preventDefault()
    setError(null)

    const cleanedFacts = facts.map((fact) => fact.trim()).filter((fact) => fact.length > 0)
    if (!name.trim() || !goal.trim() || cleanedFacts.length === 0) {
      setError('名称、目标和至少一条事实都是必填项。')
      return
    }

    setSubmitting(true)
    try {
      const graph = await api.createProject({
        name: name.trim(),
        description: description.trim() || undefined,
        goal: goal.trim(),
        facts: cleanedFacts,
      })
      navigate(`/projects/${graph.id}`)
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : '创建项目失败。')
      setSubmitting(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h1 className="text-2xl font-semibold text-fg md:text-3xl">新建项目</h1>
        <p className="mt-1 text-sm text-fg-muted">
          项目从开放状态开始：先写下目标与已知事实，再通过步骤把它们连起来。
        </p>
      </div>

      {error ? <Alert tone="danger">{error}</Alert> : null}

      <form className="flex max-w-3xl flex-col gap-4" onSubmit={handleSubmit}>
        <Card className="flex flex-col gap-4 p-4">
          <TextInput
            label="项目名称"
            onChange={(event) => setName(event.target.value)}
            placeholder="例如：产品发布"
            value={name}
          />
          <TextInput
            hint="可选，用于补充背景。"
            label="描述"
            onChange={(event) => setDescription(event.target.value)}
            placeholder="一句话说明这个项目的背景"
            value={description}
          />
          <TextArea
            hint="项目结束时希望达成的状态，完成项目时它将作为终点节点。"
            label="目标"
            onChange={(event) => setGoal(event.target.value)}
            placeholder="例如：发布产品"
            rows={2}
            value={goal}
          />
        </Card>

        <Card className="flex flex-col gap-3 p-4">
          <div className="flex items-center justify-between">
            <p className="text-sm font-semibold text-fg">初始事实</p>
            <Button onClick={addFact} size="sm">
              <IconPlus className="h-3.5 w-3.5" />
              添加事实
            </Button>
          </div>
          <p className="-mt-1 text-xs text-fg-muted">
            至少需要一条事实。事实是图谱中的起点，步骤只能从事实出发。
          </p>
          <div className="flex flex-col gap-2">
            {facts.map((fact, index) => (
              <div className="flex items-start gap-2" key={index}>
                <div className="flex-1">
                  <TextArea
                    aria-label={`事实 ${index + 1}`}
                    label={`事实 ${index + 1}`}
                    onChange={(event) => updateFact(index, event.target.value)}
                    placeholder="描述一件已知或已完成的事"
                    rows={2}
                    value={fact}
                  />
                </div>
                {facts.length > 1 ? (
                  <Button
                    aria-label={`删除事实 ${index + 1}`}
                    className="mt-6"
                    onClick={() => removeFact(index)}
                    size="sm"
                    variant="danger"
                  >
                    <IconTrash className="h-3.5 w-3.5" />
                  </Button>
                ) : null}
              </div>
            ))}
          </div>
        </Card>

        <div className="flex items-center gap-2">
          <Button disabled={submitting} type="submit" variant="primary">
            {submitting ? '创建中…' : '创建项目'}
          </Button>
          <Link to="/">
            <Button>取消</Button>
          </Link>
        </div>
      </form>
    </div>
  )
}
