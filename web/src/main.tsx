import { QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from 'react-router/dom'
import { createQueryClient } from './app/queryClient'
import { createRouter } from './app/router'
import { initTheme } from './app/theme'
import { UpdatePrompt } from './pwa/UpdatePrompt'
import { Toaster } from './ui'
import './styles/app.css'

initTheme()

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')

const queryClient = createQueryClient()
const router = createRouter()

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      <Toaster />
      <UpdatePrompt />
    </QueryClientProvider>
  </StrictMode>,
)
