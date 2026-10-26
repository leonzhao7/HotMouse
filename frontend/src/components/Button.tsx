import type { ButtonHTMLAttributes, ReactNode } from 'react'

type Variant = 'primary' | 'secondary' | 'danger' | 'outline'
type Size = 'sm' | 'md'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
  children: ReactNode
}

/* 配方来源: github-style.md / "Component Recipes" -> Button
 * Hover: hover:brightness-95 + 极轻阴影；Active: 轻微按压反馈。 */
const VARIANTS: Record<Variant, string> = {
  primary: 'bg-success text-white border-success hover:brightness-95',
  secondary: 'bg-canvas-subtle text-nav border-border hover:bg-border/40',
  danger: 'bg-danger text-white border-danger hover:brightness-95',
  outline: 'bg-transparent text-accent border-border hover:bg-canvas-subtle',
}

const SIZES: Record<Size, string> = {
  sm: 'text-xs px-2 py-1',
  md: 'text-sm px-3 py-1.5',
}

export function Button({
  variant = 'secondary',
  size = 'md',
  className = '',
  children,
  ...props
}: ButtonProps) {
  return (
    <button
      className={`inline-flex items-center justify-center gap-1.5 rounded-md border font-medium leading-5 transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-60 ${VARIANTS[variant]} ${SIZES[size]} ${className}`}
      type="button"
      {...props}
    >
      {children}
    </button>
  )
}
