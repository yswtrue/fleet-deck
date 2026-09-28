// antd v5 targets React 16-18; this official patch makes the static
// message/notification/Modal APIs render under React 19. Import it before any
// component that calls message.* — must stay the first app import.
import '@ant-design/v5-patch-for-react-19'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { QueryClientProvider } from '@tanstack/react-query'
import App from './App.tsx'
import { WebSocketProvider } from './hooks/WebSocketProvider.tsx'
import { queryClient } from './queryClient.ts'
import './index.css'

// One cache for the whole app. `WebSocketProvider` (below) writes directly to
// this singleton and `RefineProvider` is configured with the exact same
// instance, so Refine queries and WS-driven invalidations share one cache.
// Do not introduce a second QueryClient.
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <WebSocketProvider>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </WebSocketProvider>
    </QueryClientProvider>
  </StrictMode>,
)
