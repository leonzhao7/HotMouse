import { useId } from 'react'
import type { InputHTMLAttributes, ReactNode, TextareaHTMLAttributes } from 'react'

/* 配方来源: github-style.md / "Component Recipes" -> Input */

const FIELD_CLASS =
  'w-full rounded-md border border-border bg-canvas-subtle px-3 py-2 text-sm text-nav placeholder:text-fg-subtle transition-colors duration-150 focus:bg-canvas focus:outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60'

interface FieldProps {
  label: string
  hint?: ReactNode
  children: ReactNode
  htmlFor?: string
}

export function Field({ label, hint, children, htmlFor }: FieldProps) {
  return (
    <div className="flex flex-col gap-1">
      <label className="text-sm font-semibold text-fg" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {hint ? <p className="text-xs text-fg-muted">{hint}</p> : null}
    </div>
  )
}

interface TextInputProps extends InputHTMLAttributes<HTMLInputElement> {
  label: string
  hint?: ReactNode
}

export function TextInput({ label, hint, id, className = '', ...props }: TextInputProps) {
  const generatedId = useId()
  const inputId = id ?? generatedId
  return (
    <Field hint={hint} htmlFor={inputId} label={label}>
      <input className={`${FIELD_CLASS} ${className}`} id={inputId} {...props} />
    </Field>
  )
}

interface TextAreaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  label: string
  hint?: ReactNode
}

export function TextArea({ label, hint, id, className = '', ...props }: TextAreaProps) {
  const generatedId = useId()
  const inputId = id ?? generatedId
  return (
    <Field hint={hint} htmlFor={inputId} label={label}>
      <textarea className={`${FIELD_CLASS} resize-y ${className}`} id={inputId} {...props} />
    </Field>
  )
}
