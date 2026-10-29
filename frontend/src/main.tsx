import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider, createBrowserRouter } from 'react-router-dom'

import { ErrorPage } from '@/components/ErrorPage'
import { Layout } from '@/components/Layout'
import { NewProjectPage } from '@/pages/NewProjectPage'
import { ProjectDetailPage } from '@/pages/ProjectDetailPage'
import { ProjectListPage } from '@/pages/ProjectListPage'
import { SettingsPage } from '@/pages/SettingsPage'
import { LLMLogsPage } from '@/pages/LLMLogsPage'

import './index.css'

const router = createBrowserRouter([
  {
    path: '/',
    element: <Layout />,
    errorElement: <ErrorPage />,
    children: [
      { index: true, element: <ProjectListPage /> },
      { path: 'new', element: <NewProjectPage /> },
      { path: 'projects/:id', element: <ProjectDetailPage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: 'logs', element: <LLMLogsPage /> },
    ],
  },
])

const container = document.getElementById('root')
if (!container) {
  throw new Error('找不到 #root 挂载点。')
}

createRoot(container).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)
