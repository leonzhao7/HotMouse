import type { ReactNode } from 'react'

type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'accent' | 'info'

const TONES: Record<Tone, string> = {
  neutral: 'border-border bg-canvas-subtle text-fg-muted',
  success: 'border-success/30 bg-success/10 text-success',
  warning: 'border-warning/30 bg-warning/10 text-warning',
  danger: 'border-danger/30 bg-danger/10 text-danger',
  accent: 'border-accent/30 bg-accent/10 text-accent',
  info: 'border-info/30 bg-info/10 text-info',
}

interface BadgeProps {
  tone?: Tone
  children: ReactNode
  className?: string
}

/* 徽章允许 rounded-full —— github-style.md 中 rounded-full 仅用于头像和标签。 */
export function Badge({ tone = 'neutral', children, className = '' }: BadgeProps) {
  return (
    <span
      className={`inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium leading-4 ${TONES[tone]} ${className}`}
    >
      {children}
    </span>
  )
}
