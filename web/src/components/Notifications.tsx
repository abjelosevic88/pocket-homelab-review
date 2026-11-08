// The bell in the top bar: your notifications (mentions, replies, new
// versions, decisions, tasks), newest first. Live while a document is open,
// otherwise checked every minute and when you come back to the tab.
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import type { Notification } from '../lib/types'
import { ago } from '../lib/format'
import { Icon } from './icons'
import { Avatar } from './people'
import { Popover, rectOf, type Anchor } from './ui'

const KIND_ICON: Record<Notification['kind'], string> = {
  mention: 'at', reply: 'reply', thread: 'message', revision: 'upload', review: 'review', verdict: 'thumbs-up', task: 'check-square',
}

export function NotificationBell() {
  const navigate = useNavigate()
  const [unread, setUnread] = useState(0)
  const [items, setItems] = useState<Notification[] | null>(null)
  const [anchor, setAnchor] = useState<Anchor | null>(null)

  const count = useCallback(() => { api.get<{ unread: number }>(`${V1}/notifications/unread`).then(r => setUnread(r.unread)).catch(() => {}) }, [])
  const load = useCallback(() => api.get<{ items: Notification[]; unread: number }>(`${V1}/notifications?limit=60`).then(r => { setItems(r.items); setUnread(r.unread) }).catch(() => {}), [])
  useEffect(() => {
    count()
    const t = setInterval(count, 60000)
    const vis = () => { if (document.visibilityState === 'visible') count() }
    const live = () => { count(); if (anchor) load() }
    document.addEventListener('visibilitychange', vis)
    window.addEventListener('pcl:inbox', live)
    return () => { clearInterval(t); document.removeEventListener('visibilitychange', vis); window.removeEventListener('pcl:inbox', live) }
  }, [count, load, anchor])
  useEffect(() => {
    const n = unread > 0 ? `(${unread}) ` : ''
    document.title = n + document.title.replace(/^\(\d+\) /, '')
  }, [unread])

  const read = (body: object) => api.post<{ unread: number }>(`${V1}/notifications/read`, body).then(r => setUnread(r.unread)).catch(() => {})
  const open = (n: Notification) => {
    setAnchor(null)
    if (!n.read_at) { read({ ids: [n.id] }); setItems(list => list?.map(x => x.id === n.id ? { ...x, read_at: new Date().toISOString() } : x) ?? null) }
    if (n.review_id) navigate(`/reviews/${n.review_id}${n.comment_id ? `?c=${n.comment_id}` : ''}`)
  }
  const allRead = () => { read({}); setItems(list => list?.map(x => ({ ...x, read_at: x.read_at || new Date().toISOString() })) ?? null) }

  return (
    <>
      <button type="button" className="btn ghost icon-only bell-btn" aria-label={unread ? `Notifications, ${unread} unread` : 'Notifications'} title="Notifications"
        onClick={e => { if (anchor) { setAnchor(null); return } setAnchor(rectOf(e.currentTarget)); setItems(null); load() }}>
        <Icon name="bell" size={18} />
        {unread > 0 && <span className="bell-count">{unread > 99 ? '99+' : unread}</span>}
      </button>
      {anchor && (
        <Popover anchor={{ ...anchor, x: anchor.x + anchor.w - 380 }} prefer="below" width={380} onClose={() => setAnchor(null)} className="notif-pop" label="Notifications">
          <div className="notif-head">
            <b>Notifications</b>
            <span className="spacer" />
            {unread > 0 && <button type="button" className="btn ghost sm" onClick={allRead}><Icon name="check" size={14} />Mark all read</button>}
            <button type="button" className="btn ghost icon-only sm" title="Notification settings" aria-label="Notification settings" onClick={() => { setAnchor(null); navigate('/settings/notifications') }}><Icon name="settings" size={15} /></button>
          </div>
          <div className="notif-list">
            {items === null ? <div className="muted small notif-empty">Loading…</div>
              : items.length === 0 ? <div className="notif-empty"><Icon name="bell" size={22} /><span className="muted small">Nothing yet. You'll hear here when someone mentions you, replies, uploads a new version or decides.</span></div>
              : items.map(n => (
                <button key={n.id} type="button" className={`notif ${n.read_at ? '' : 'unread'}`} onClick={() => open(n)}>
                  <span className="notif-ava">
                    {n.actor_id ? <Avatar id={n.actor_id} name={n.actor_name || '?'} size="sm" /> : <span className="avatar sm sys"><Icon name="review" size={13} /></span>}
                    <span className="notif-kind"><Icon name={KIND_ICON[n.kind] || 'bell'} size={10} strokeWidth={2.4} /></span>
                  </span>
                  <span className="grow">
                    <span className="notif-title">{n.title}</span>
                    {n.body && <span className="notif-body">{n.body}</span>}
                    <span className="muted small">{n.review_title || 'A deleted review'} · {ago(n.created_at)}</span>
                  </span>
                  {!n.read_at && <span className="notif-dot" aria-label="Unread" />}
                </button>
              ))}
          </div>
        </Popover>
      )}
    </>
  )
}
