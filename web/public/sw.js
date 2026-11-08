// Pocket Review service worker: notifications on the phone (Web Push).
// Nothing is cached — Review always opens fresh from the box.

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', e => { e.waitUntil(self.clients.claim()) })

self.addEventListener('push', e => {
  let d = {}
  try { d = e.data ? e.data.json() : {} } catch { d = { body: e.data ? e.data.text() : '' } }
  const title = d.title || 'Pocket Review'
  e.waitUntil(self.registration.showNotification(title, {
    body: d.body || '',
    icon: '/icon-192.png',
    badge: '/icon-192.png',
    tag: d.tag || undefined,
    renotify: !!d.tag,
    data: { url: d.url || '/' },
  }))
})

self.addEventListener('notificationclick', e => {
  e.notification.close()
  const url = new URL((e.notification.data && e.notification.data.url) || '/', self.location.origin)
  const target = url.origin === self.location.origin ? url.href : self.location.origin + '/'
  e.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
    for (const w of wins) {
      if (new URL(w.url).origin === self.location.origin) {
        await w.focus()
        if ('navigate' in w) await w.navigate(target).catch(() => {})
        return
      }
    }
    await self.clients.openWindow(target)
  })())
})
