import type { HTMLAttributes, ReactNode } from 'react'

interface CardProps extends HTMLAttributes<HTMLDivElement> {
  children: ReactNode
}

/* 配方来源: github-style.md / "Component Recipes" -> Card */
export function Card({ className = '', children, ...props }: CardProps) {
  return (
    <div
      className={`rounded-md border border-border bg-canvas text-nav ${className}`}
      {...props}
    >
      {children}
    </div>
  )
}
