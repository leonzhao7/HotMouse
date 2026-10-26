import { Link, isRouteErrorResponse, useRouteError } from 'react-router-dom'

import { Alert } from '@/components/Alert'
import { Button } from '@/components/Button'
import { Card } from '@/components/Card'

export function ErrorPage() {
  const error = useRouteError()

  let title = '页面出错了'
  let message = '发生了未预期的错误。'
  if (isRouteErrorResponse(error)) {
    title = `${error.status} ${error.statusText}`
    message = '请求的页面不存在。'
  } else if (error instanceof Error) {
    message = error.message
  }

  return (
    <div className="mx-auto flex max-w-xl flex-col gap-4 py-16">
      <Card className="flex flex-col gap-3 p-4">
        <h1 className="text-2xl font-semibold text-fg">{title}</h1>
        <Alert tone="danger">{message}</Alert>
        <Link className="self-start" to="/">
          <Button variant="primary">返回项目列表</Button>
        </Link>
      </Card>
    </div>
  )
}
