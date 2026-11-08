import { api, V1 } from './api'

// Notifications in this browser or on this phone (Web Push through Review's own service worker).

export type PushState = 'unsupported' | 'ios-home' | 'denied' | 'off' | 'on'

const isIOS = () => /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
const standalone = () => window.matchMedia('(display-mode: standalone)').matches || (navigator as unknown as { standalone?: boolean }).standalone === true

export function pushSupported() {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

async function registration() {
  return navigator.serviceWorker.register('/sw.js')
}

export async function pushState(): Promise<PushState> {
  if (!pushSupported()) return isIOS() && !standalone() ? 'ios-home' : 'unsupported'
  if (Notification.permission === 'denied') return 'denied'
  const reg = await navigator.serviceWorker.getRegistration('/')
  const sub = reg ? await reg.pushManager.getSubscription() : null
  return sub && Notification.permission === 'granted' ? 'on' : 'off'
}

function keyBytes(b64: string) {
  const pad = '='.repeat((4 - (b64.length % 4)) % 4)
  const raw = atob((b64 + pad).replace(/-/g, '+').replace(/_/g, '/'))
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

export async function pushOn(): Promise<PushState> {
  if (!pushSupported()) return pushState()
  const perm = await Notification.requestPermission()
  if (perm !== 'granted') return perm === 'denied' ? 'denied' : 'off'
  const reg = await registration()
  await navigator.serviceWorker.ready
  const { public_key } = await api.get<{ public_key: string }>(`${V1}/push/key`)
  let sub = await reg.pushManager.getSubscription()
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(public_key) })
  await api.post(`${V1}/push/subscribe`, sub.toJSON())
  return 'on'
}

export async function pushOff(): Promise<PushState> {
  const reg = await navigator.serviceWorker.getRegistration('/')
  const sub = reg ? await reg.pushManager.getSubscription() : null
  if (sub) {
    await api.post(`${V1}/push/unsubscribe`, { endpoint: sub.endpoint }).catch(() => {})
    await sub.unsubscribe().catch(() => {})
  }
  return 'off'
}

/** Keep this phone's subscription on the box (it can change or be forgotten). */
export async function pushRefresh() {
  if (!pushSupported() || Notification.permission !== 'granted') return
  const reg = await navigator.serviceWorker.getRegistration('/')
  const sub = reg ? await reg.pushManager.getSubscription() : null
  if (sub) await api.post(`${V1}/push/subscribe`, sub.toJSON()).catch(() => {})
}
