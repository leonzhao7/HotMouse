export function Spinner({ label = '加载中' }: { label?: string }) {
  return (
    <span className="inline-flex items-center gap-2 text-sm text-fg-muted">
      <span
        aria-hidden="true"
        className="h-4 w-4 animate-spin rounded-full border-2 border-border border-t-accent"
      />
      {label}
    </span>
  )
}

export function PageLoading() {
  return (
    <div className="flex justify-center py-16">
      <Spinner />
    </div>
  )
}
