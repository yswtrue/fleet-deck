/**
 * PWA install affordance state. Browsers that support installation fire
 * `beforeinstallprompt`; we defer it so an explicit in-app button can call
 * `prompt()` later. iOS never fires the event, so that platform gets the
 * manual "Share -> Add to Home Screen" instructions instead.
 */

export type InstallState = 'available' | 'ios' | 'installed' | 'unavailable'

/** The non-standard Chromium event carrying a one-shot install prompt. */
interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed'; platform: string }>
}

const listeners = new Set<() => void>()
let deferred: BeforeInstallPromptEvent | null = null
let installed = false
let initialized = false

function emit(): void {
  for (const listener of [...listeners]) listener()
}

function isStandalone(): boolean {
  if (typeof window === 'undefined') return false
  const nav = window.navigator as Navigator & { standalone?: boolean }
  if (nav.standalone === true) return true
  return window.matchMedia?.('(display-mode: standalone)').matches === true
}

function isIOS(): boolean {
  if (typeof window === 'undefined') return false
  const nav = window.navigator as Navigator & { maxTouchPoints?: number }
  const ua = nav.userAgent || ''
  // iPadOS 13+ reports a desktop Macintosh UA; the touch count identifies it.
  return /iPad|iPhone|iPod/.test(ua) || (/Macintosh/.test(ua) && (nav.maxTouchPoints ?? 0) > 1)
}

/** Current install affordance for this browser. */
export function installState(): InstallState {
  if (installed || isStandalone()) return 'installed'
  if (deferred) return 'available'
  if (isIOS()) return 'ios'
  return 'unavailable'
}

/**
 * Starts listening for the install lifecycle. Idempotent, and safe to call at
 * module scope: the event can fire before the first React render.
 */
export function initInstallPrompt(): void {
  if (initialized || typeof window === 'undefined') return
  initialized = true
  window.addEventListener('beforeinstallprompt', (event) => {
    // Suppress the browser's own mini-infobar in favor of our button.
    event.preventDefault()
    deferred = event as BeforeInstallPromptEvent
    emit()
  })
  window.addEventListener('appinstalled', () => {
    deferred = null
    installed = true
    emit()
  })
}

/** Subscribes to install-state changes; used with useSyncExternalStore. */
export function subscribeInstall(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/**
 * Opens the browser's install prompt. The deferred event is one-shot, so it is
 * cleared before prompting; Chrome may fire a fresh one if the user declines.
 */
export async function promptInstall(): Promise<'accepted' | 'dismissed' | 'unavailable'> {
  const event = deferred
  if (!event) return 'unavailable'
  deferred = null
  emit()
  await event.prompt()
  const choice = await event.userChoice
  if (choice.outcome === 'accepted') {
    installed = true
    emit()
  }
  return choice.outcome
}

// Capture the event as early as the bundle allows; main.tsx imports the app
// (and this module) before rendering.
initInstallPrompt()
