import { apiClient, getStoredToken, onTokenChange } from './apiClient.ts'
import { i18n } from './i18n/index.ts'

/**
 * Web Push client helpers. Push is enabled only for the built app: the service
 * worker is registered in production (see main.tsx), and dev/test
 * environments have no registration to bind a subscription to.
 */
export function pushSupported(): boolean {
  return (
    import.meta.env.PROD &&
    typeof window !== 'undefined' &&
    'serviceWorker' in navigator &&
    'PushManager' in window &&
    typeof Notification !== 'undefined'
  )
}

async function registration(): Promise<ServiceWorkerRegistration | null> {
  if (!pushSupported()) return null
  return (await navigator.serviceWorker.getRegistration()) ?? null
}

/** The browser's current push subscription, or null when none exists. */
export async function getPushSubscription(): Promise<PushSubscription | null> {
  try {
    const reg = await registration()
    if (!reg) return null
    return await reg.pushManager.getSubscription()
  } catch {
    return null
  }
}

async function waitForRegistration(): Promise<ServiceWorkerRegistration> {
  const existing = await registration()
  if (existing) return existing
  // Registration is deferred to window load; wait for it, but never hang the
  // settings panel when the worker failed to install. ES2023 lacks
  // Promise.withResolvers, so this is the one executor-form promise here.
  let timer = 0
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error('service worker is not ready')), 5000)
  })
  try {
    return await Promise.race([navigator.serviceWorker.ready, timeout])
  } finally {
    clearTimeout(timer)
  }
}

function applicationServerKey(publicKey: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (publicKey.length % 4)) % 4)
  const raw = atob((publicKey + padding).replace(/-/g, '+').replace(/_/g, '/'))
  const bytes = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i += 1) bytes[i] = raw.charCodeAt(i)
  return bytes
}

/** True when the browser's subscription was created with this VAPID key. */
function subscribedWith(
  applicationServerKey: ArrayBuffer | null | undefined,
  key: Uint8Array<ArrayBuffer>
): boolean {
  if (!applicationServerKey) return false
  const current = new Uint8Array(applicationServerKey)
  return current.length === key.length && current.every((byte, i) => byte === key[i])
}

function subscriptionLang(): string {
  const lang = i18n.language || 'en'
  return lang.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

async function save(sub: PushSubscription): Promise<void> {
  const json = sub.toJSON()
  const p256dh = json.keys?.p256dh
  const auth = json.keys?.auth
  if (!p256dh || !auth) throw new Error('push subscription has no keys')
  await apiClient.savePushSubscription({
    endpoint: sub.endpoint,
    p256dh,
    auth,
    lang: subscriptionLang(),
  })
}

/**
 * Ensures this browser holds a push subscription and that the hub knows about
 * it. Re-registering an existing subscription re-binds the endpoint to the
 * signed-in user.
 */
export async function enablePushNotifications(): Promise<PushSubscription> {
  const reg = await waitForRegistration()
  const { public_key } = await apiClient.getPushPublicKey()
  if (!public_key) throw new Error('hub returned no VAPID public key')
  const key = applicationServerKey(public_key)

  let existing = await reg.pushManager.getSubscription()
  if (existing && !subscribedWith(existing.options.applicationServerKey, key)) {
    // The hub rotated its VAPID key: the old subscription can never be
    // delivered to again, so replace it instead of reporting a false "on".
    await existing.unsubscribe()
    existing = null
  }
  if (existing) {
    await save(existing)
    return existing
  }
  const sub = await reg.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: key,
  })
  await save(sub)
  return sub
}

/**
 * Removes the server-side registration (while a token is available) and
 * unsubscribes the browser. Best-effort: a failed DELETE must not leave the
 * browser subscribed, or the next account on this device could receive the
 * previous user's notifications.
 */
export async function disablePushNotifications(): Promise<void> {
  const sub = await getPushSubscription()
  if (!sub) return
  try {
    if (getStoredToken()) await apiClient.deletePushSubscription(sub.endpoint)
  } finally {
    try {
      await sub.unsubscribe()
    } catch {
      // Already unsubscribed / browser state lost: nothing else to do.
    }
  }
}

// A cleared token (logout, expiry, 401) must never leave a live subscription
// behind: the push service would keep delivering this device's notifications.
onTokenChange((token) => {
  if (token === null) void disablePushNotifications()
})
