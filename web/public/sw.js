/*
 * FleetDeck service worker: PWA offline shell + Web Push notifications.
 *
 * The shell is network-first for navigations (so a deployed update lands on the
 * next online load) and cache-first for hashed build assets (immutable). API
 * and WebSocket traffic is never cached. Push payloads come from the hub as
 * structured JSON (see internal/hub/push.go); notification copy is localized
 * here from the subscription's language.
 */

const CACHE_NAME = 'fleetdeck-shell-v1'
const SHELL_URLS = [
  '/',
  '/index.html',
  '/manifest.webmanifest',
  '/favicon.png',
  '/icons/icon-192.png',
  '/icons/icon-512.png',
  '/icons/icon-512-maskable.png',
  '/icons/apple-touch-icon.png',
]

const COPY = {
  en: {
    fallbackTitle: 'FleetDeck',
    fallbackBody: 'New task activity',
    status: {
      waiting: 'Task waiting',
      running: 'Task started',
      awaiting_user: 'Task needs your input',
      done: 'Task done',
      failed: 'Task failed',
    },
    updated: 'Task updated',
  },
  zh: {
    fallbackTitle: 'FleetDeck',
    fallbackBody: '有新的任务动态',
    status: {
      waiting: '任务等待中',
      running: '任务进行中',
      awaiting_user: '任务需要你的确认',
      done: '任务完成',
      failed: '任务失败',
    },
    updated: '任务状态已更新',
  },
}

function copyFor(lang) {
  return String(lang || '')
    .toLowerCase()
    .startsWith('zh')
    ? COPY.zh
    : COPY.en
}

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(SHELL_URLS))
      .then(() => self.skipWaiting())
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key)))
      )
      .then(() => self.clients.claim())
  )
})

self.addEventListener('fetch', (event) => {
  const request = event.request
  if (request.method !== 'GET') return

  const url = new URL(request.url)
  if (url.origin !== self.location.origin) return
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/ws/')) return
  // Runtime config may point at a different hub; never serve it stale.
  if (url.pathname === '/config.js') return

  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request)
        .then((response) => {
          if (response.ok) {
            const copy = response.clone()
            caches.open(CACHE_NAME).then((cache) => cache.put('/index.html', copy))
          }
          return response
        })
        .catch(() => caches.match('/index.html').then((cached) => cached || Response.error()))
    )
    return
  }

  event.respondWith(
    caches.match(request).then((cached) => {
      if (cached) return cached
      return fetch(request).then((response) => {
        if (response.ok) {
          const copy = response.clone()
          caches.open(CACHE_NAME).then((cache) => cache.put(request, copy))
        }
        return response
      })
    })
  )
})

self.addEventListener('push', (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    data = {}
  }

  const copy = copyFor(data.lang || (self.navigator && self.navigator.language))
  const status = data.to && copy.status[data.to]
  const title = data.title || status || copy.fallbackTitle
  const body = data.title ? status || copy.updated : copy.fallbackBody

  event.waitUntil(
    (async () => {
      // A focused client already shows the in-app WS notification; skip the
      // push copy so one status change never raises two notifications.
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      if (windows.some((client) => client.focused)) return

      await self.registration.showNotification(title, {
        body,
        tag: data.task_id ? 'fleetdeck-task-' + data.task_id : 'fleetdeck',
        icon: '/icons/icon-192.png',
        badge: '/favicon.png',
        lang: copy === COPY.zh ? 'zh-CN' : 'en',
        data: { url: data.url || '/' },
      })
    })()
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = (event.notification.data && event.notification.data.url) || '/'
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      for (const client of windows) {
        try {
          const focused = await client.focus()
          if (focused && focused.navigate) await focused.navigate(target)
          else if (focused) await focused.focus()
          return
        } catch {
          // Fall through to opening a new window.
        }
      }
      await self.clients.openWindow(target)
    })()
  )
})
