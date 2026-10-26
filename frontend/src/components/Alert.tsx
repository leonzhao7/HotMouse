import type { ReactNode } from 'react'

import { IconAlert, IconCheckCircle, IconInfo } from './icons'

type Tone = 'danger' | 'success' | 'warning' | 'info'

const TONES: Record<Tone, { wrap: string; icon: ReactNode }> = {
  danger: {
    wrap: 'border-danger/40 bg-danger/5 text-danger',
    icon: <IconAlert className="h-4 w-4 shrink-0" />,
  },
  success: {
    wrap: 'border-success/40 bg-success/5 text-success',
    icon: <IconCheckCircle className="h-4 w-4 shrink-0" />,
  },
  warning: {
    wrap: 'border-warning/40 bg-warning/5 text-warning',
    icon: <IconAlert className="h-4 w-4 shrink-0" />,
  },
  info: {
    wrap: 'border-accent/40 bg-accent/5 text-accent',
    icon: <IconInfo className="h-4 w-4 shrink-0" />,
  },
}

interface AlertProps {
  tone?: Tone
  title?: string
  children: ReactNode
  className?: string
}

export function Alert({ tone = 'info', title, children, className = '' }: AlertProps) {
  const { wrap, icon } = TONES[tone]
  return (
    <div className={`flex gap-2 rounded-md border p-3 text-sm ${wrap} ${className}`} role="alert">
      {icon}
      <div className="min-w-0">
        {title ? <p className="font-semibold">{title}</p> : null}
        <div className="break-words">{children}</div>
      </div>
    </div>
  )
}
