import { NavLink, Outlet, Link } from 'react-router-dom'

import { IconRepo } from './icons'

const NAV_ITEMS = [
  { to: '/', label: '项目', end: true },
  { to: '/new', label: '新建项目', end: false },
  { to: '/settings', label: '设置', end: false },
]

export function Layout() {
  return (
    <div className="flex min-h-screen flex-col bg-canvas">
      <header className="bg-nav text-white">
        <div className="mx-auto flex h-12 max-w-6xl items-center gap-4 px-4 md:px-6">
          <Link className="flex items-center gap-2 text-sm font-semibold" to="/">
            <IconRepo className="h-4 w-4" />
            HotMouse
          </Link>
          <nav className="flex items-center gap-1">
            {NAV_ITEMS.map((item) => (
              <NavLink
                className={({ isActive }) =>
                  `rounded-md px-2 py-1 text-sm transition-colors duration-150 ${
                    isActive
                      ? 'bg-nav-hover font-semibold text-white'
                      : 'text-white/75 hover:bg-nav-hover hover:text-white'
                  }`
                }
                end={item.end}
                key={item.to}
                to={item.to}
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
        </div>
      </header>

      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 md:px-6 md:py-8">
        <Outlet />
      </main>

      <footer className="border-t border-border bg-canvas-subtle">
        <div className="mx-auto max-w-6xl px-4 py-4 text-xs text-fg-muted md:px-6">
          HotMouse — 事实图谱决策看板 · 数据来自本地 Go API
        </div>
      </footer>
    </div>
  )
}
