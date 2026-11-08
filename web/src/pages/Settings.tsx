import { AskButton } from '../components/AskButton'
import React, { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { APIToken, Settings as SettingsT, User } from '../lib/types'
import { Confirm, Field, Loading, Modal, Segmented, SettingRow, Switch, Tabs, useAsync, useIsMobile, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { Avatar } from '../components/people'
import { Select } from '../components/Select'
import { ShortcutsSheet } from '../components/ShortcutsSheet'
import { pushOff, pushOn, pushState, type PushState } from '../lib/push'
import { DATE_FORMATS, TIME_FORMATS, fmtBytes, fmtDateTime, fmtYMD, fmtTime } from '../lib/format'

type TabId = 'general' | 'notifications' | 'profile' | 'people' | 'tokens' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string }[] = [
  { id: 'general', label: 'General', icon: 'settings', sub: 'Date and time formats, the app title, and how review time is measured.' },
  { id: 'notifications', label: 'Notifications', icon: 'bell', sub: 'What you hear about, in the bell and on this browser or phone.' },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, email and password.' },
  { id: 'people', label: 'People', icon: 'users', sub: 'Everyone who has an account on this box.' },
  { id: 'tokens', label: 'API tokens', icon: 'key', sub: 'Personal tokens for scripts and a Homepage widget.' },
  { id: 'about', label: 'About', icon: 'info', sub: 'Version, numbers and the server.' },
]
// the old ?tab= names
const ALIAS: Record<string, TabId> = { users: 'people', api: 'tokens' }

export default function Settings() {
  const { tab: raw } = useParams()
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const legacy = new URLSearchParams(window.location.search).get('tab') || ''
  const want = raw || legacy
  const tab: TabId = (TABS.find(t => t.id === (ALIAS[want] || want))?.id) || 'general'
  const current = TABS.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })
  const [shortcuts, setShortcuts] = useState(false)

  // /settings?tab=api → /settings/tokens
  useEffect(() => { if (legacy && !raw) navigate(`/settings/${tab}`, { replace: true }) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.querySelector('.modal-backdrop, .menu, .sel-pop')) navigate('/') }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate])

  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back to the reviews" title="Back (esc)" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
        <span style={{ flex: 1 }} />
        <AskButton />
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={TABS.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {TABS.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner" key={tab}>
            <div className="set-title">
              <h2>{current.label}</h2>
              <p>{current.sub}</p>
            </div>
            {tab === 'general' && <General />}
            {tab === 'notifications' && <Notifications />}
            {tab === 'profile' && <Profile />}
            {tab === 'people' && <Users />}
            {tab === 'tokens' && <Tokens />}
            {tab === 'about' && <About onShortcuts={() => setShortcuts(true)} />}
          </div>
        </main>
      </div>
      {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
    </div>
  )
}

/** A settings card: title, explanation, actions on the right — Pocket Calendar's section. */
function Section({ title, sub, icon, actions, children }: { title: React.ReactNode; sub?: React.ReactNode; icon?: string; actions?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        {icon && <span className="set-app-ico"><Icon name={icon} size={17} /></span>}
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children && <div className="set-section-body">{children}</div>}
    </section>
  )
}

function General() {
  const { settings, setSettings, user } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState<SettingsT | null>(settings)
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!f && settings) setF(settings) }, [settings]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!f) return <Loading />
  const isAdmin = user?.role === 'admin'
  const save = async () => {
    setBusy(true)
    try { const s = await api.put<SettingsT>(`${V1}/settings`, f); setSettings(s); setF(s); toast('Saved') } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  const now = new Date()
  return (
    <div className="stack loose">
      <Section title="Date & time" sub="Yours alone — everyone on the box picks their own.">
        <SettingRow title="Date format" help={`Today: ${fmtYMD(now.getFullYear(), now.getMonth() + 1, now.getDate(), true, f.date_format)}`}>
          <Select value={f.date_format || 'auto'} onChange={v => setF({ ...f, date_format: v as SettingsT['date_format'] })} options={DATE_FORMATS.map(d => ({ value: d.id, label: d.label }))} ariaLabel="Date format" />
        </SettingRow>
        <SettingRow title="Time format" help={`Now: ${fmtTime(now, f.time_format)}`}>
          <Select value={f.time_format || 'auto'} onChange={v => setF({ ...f, time_format: v as SettingsT['time_format'] })} options={TIME_FORMATS.map(t => ({ value: t.id, label: t.label }))} ariaLabel="Time format" />
        </SettingRow>
      </Section>
      <Section title="This box" sub={isAdmin ? 'This applies to everyone on this box.' : 'Set by an admin for everyone.'}>
        <SettingRow title="App title" help="Shown in the sidebar and the browser tab."><input value={f.title} onChange={e => setF({ ...f, title: e.target.value })} maxLength={40} disabled={!isAdmin} style={{ width: 220 }} aria-label="App title" /></SettingRow>
      </Section>
      <div className="row"><button type="button" className="btn primary" onClick={save} disabled={busy}>Save changes</button></div>
      <Section title="How review time is measured" icon="clock">
        <p className="muted">The server keeps the clock, not the browser. Time counts while a document is open in a visible tab and you did something with it (scrolled, moved the mouse, typed, zoomed) in the last {Math.round((f.idle_after_seconds || 120) / 60)} minute(s). Several tabs count once. The time goes to the page in view, which is how the report knows which pages each person actually read.</p>
        <p className="muted small">Uploads up to {f.max_upload_mb} MB. Set by whoever runs the box (<code>IDLE_AFTER</code>, <code>MAX_UPLOAD_MB</code>).</p>
      </Section>
    </div>
  )
}

const NOTIFY_KINDS: { id: string; title: string; help: string }[] = [
  { id: 'mention', title: 'Mentions', help: 'Someone writes @you or @everyone in a thread.' },
  { id: 'reply', title: 'Your threads', help: 'A reply in a thread you wrote in; your thread resolved, rejected or reopened.' },
  { id: 'revision', title: 'New versions', help: 'A new version of a document you review is uploaded.' },
  { id: 'review', title: 'Reviews', help: 'You are added to a review; it starts, is approved, rejected, cancelled or reopened, or changes are requested.' },
  { id: 'task', title: 'Tasks', help: 'A task is given to you.' },
  { id: 'thread', title: 'New threads on your reviews', help: 'Someone comments, asks or raises an issue on a review you own.' },
  { id: 'verdict', title: 'Verdicts on your reviews', help: 'A reviewer finishes or an approver decides on a review you own.' },
]

const PUSH_WORDS: Record<PushState, string> = {
  unsupported: 'This browser cannot show notifications from Pocket Review.',
  'ios-home': 'On an iPhone: Share → “Add to Home Screen”, open Pocket Review from there, then switch this on.',
  denied: 'Notifications are blocked for Pocket Review in this browser’s settings. Allow them there, then switch this on.',
  off: 'Off — you still see everything in the bell.',
  on: 'On — what you switch on below also pops up here, even with Pocket Review closed.',
}

function Notifications() {
  const { toast } = useToast()
  const [off, setOff] = useState<string[] | null>(null)
  const [push, setPush] = useState<PushState | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api.get<{ off: string[] }>(`${V1}/notifications/settings`).then(r => setOff(r.off)).catch(e => toast((e as Error).message, 'err'))
    pushState().then(setPush)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  if (!off) return <Loading />
  const flipKind = async (id: string, on: boolean) => {
    const next = on ? off.filter(k => k !== id) : [...off, id]
    setOff(next)
    try { await api.put(`${V1}/notifications/settings`, { off: next }) } catch (e) { toast((e as Error).message, 'err') }
  }
  const flipPush = async (v: boolean) => {
    setBusy(true)
    try { setPush(v ? await pushOn() : await pushOff()) } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  const test = async () => {
    try { const r = await api.post<{ ok: boolean; message?: string; error?: string }>(`${V1}/push/test`); toast(r.ok ? r.message || 'Sent' : r.error || 'Not sent', r.ok ? 'ok' : 'err') } catch (e) { toast((e as Error).message, 'err') }
  }
  return (
    <div className="stack loose">
      <Section title="On this browser or phone" icon="bell" sub="Pop-up notifications, also when Pocket Review is closed. Switch it on on every device you want them on."
        actions={push === 'on' && <button type="button" className="btn sm" onClick={test}>Send a test</button>}>
        <SettingRow title="Notifications here" help={push ? PUSH_WORDS[push] : '…'}>
          <Switch checked={push === 'on'} onChange={flipPush} label="Notifications here" disabled={busy || !push || push === 'unsupported' || push === 'ios-home' || push === 'denied'} />
        </SettingRow>
      </Section>
      <Section title="Tell me about" sub="Each goes to the bell (top right) and, when switched on above, to this browser or phone. You never hear about what you did yourself.">
        {NOTIFY_KINDS.map(k => (
          <SettingRow key={k.id} title={k.title} help={k.help}>
            <Switch checked={!off.includes(k.id)} onChange={v => flipKind(k.id, v)} label={k.title} />
          </SettingRow>
        ))}
      </Section>
    </div>
  )
}

function Users() {
  const { user, users, reloadUsers, status } = useApp()
  const { toast } = useToast()
  const [edit, setEdit] = useState<Partial<User> | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const isAdmin = user?.role === 'admin'
  const run = async (fn: () => Promise<unknown>, msg: string) => { try { await fn(); await reloadUsers(); toast(msg) } catch (e) { toast((e as Error).message, 'err') } }
  return (
    <Section title="People on this box" actions={isAdmin && <button type="button" className="btn primary sm" onClick={() => setEdit({ role: 'member' })}><Icon name="plus" size={14} />Add person</button>}
      sub="Everyone signs in with their own account and sees only the reviews they are on. Review owners can also invite people by email right from a review; those accounts have no password until an admin sets one (or they use single sign-on). Admins manage people and the title; they do not see other people's reviews.">
      <div className="set-people">
        {users.map(u => (
          <div key={u.id} className="set-person">
            <Avatar id={u.id} name={u.name || u.email} />
            <div className="grow">
              <div className="set-person-name">{u.name || <span className="muted">—</span>}{u.id === user?.id && <span className="badge">you</span>}{u.role === 'admin' && <span className="badge accent">admin</span>}</div>
              <div className="muted small ellipsis">{u.email}</div>
            </div>
            {isAdmin && <span className="row" style={{ justifyContent: 'flex-end' }}><button type="button" className="btn ghost sm" onClick={() => setEdit(u)}>Edit</button>{u.id !== user?.id && <button type="button" className="btn ghost sm danger-text" onClick={() => setDel(u)}>Remove</button>}</span>}
          </div>
        ))}
      </div>
      {status?.auth_mode !== 'local' && <p className="muted small mt">Auth mode is <code>{status?.auth_mode}</code>: users are created automatically on first visit; passwords are not used here.</p>}
      {edit && <UserModal user={edit} local={status?.auth_mode === 'local'} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reloadUsers() }} />}
      {del && <Confirm confirmLabel="Remove" title={`Remove ${del.name || del.email}?`} message="Their account and their places on reviews are removed. Their comments and log entries stay, so the record of each review is kept. Reviews they own must be handed over first." onCancel={() => setDel(null)} onConfirm={() => { const d = del; setDel(null); run(() => api.del(`${V1}/users/${d.id}`), 'User removed') }} />}
    </Section>
  )
}

function UserModal({ user, local, onClose, onSaved }: { user: Partial<User>; local: boolean; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ name: user.name || '', email: user.email || '', password: '', role: user.role || 'member' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try {
      if (user.id) await api.put(`${V1}/users/${user.id}`, f); else await api.post(`${V1}/users`, f)
      toast(user.id ? 'User saved' : 'User added'); onSaved()
    } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={user.id ? 'Edit person' : 'Add a person'} onClose={onClose} size="sm">
      <form onSubmit={save} className="grid" style={{ gap: 14 }}>
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} autoFocus required maxLength={60} /></Field>
        <Field label="Email" help="Used to sign in."><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} required /></Field>
        {local && <Field label={user.id ? 'New password (leave empty to keep)' : 'Password'} help="At least 8 characters. Tell them to change it under Settings → Profile."><input type="password" value={f.password} onChange={e => setF({ ...f, password: e.target.value })} minLength={user.id ? 0 : 8} required={!user.id} autoComplete="new-password" /></Field>}
        <Field group label="Role"><Segmented value={f.role} onChange={v => setF({ ...f, role: v })} options={[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }]} /></Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn primary" disabled={busy}>{user.id ? 'Save' : 'Add person'}</button></div>
      </form>
    </Modal>
  )
}

function Profile() {
  const { user, setUser, status } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState({ name: user?.name || '', email: user?.email || '', current_password: '', new_password: '' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { setUser(await api.put(`${V1}/auth/me`, f)); setF(x => ({ ...x, current_password: '', new_password: '' })); toast('Profile saved') } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="stack loose">
      <div className="set-profile">
        {user && <span className="set-avatar"><Avatar id={user.id} name={user.name || user.email} title="" /></span>}
        <div className="grow"><h3>{user?.name || user?.email}</h3><p className="muted small">{user?.email} · {user?.role}</p></div>
      </div>
      <Section title="You" sub="Your name is shown to everyone on your reviews.">
        <div className="stack">
          <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} /></Field>
          <Field label="Email"><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} /></Field>
        </div>
      </Section>
      {status?.auth_mode === 'local' && <Section title="Password" sub="Leave empty to keep the current one."><div className="stack">
        <Field label="Current password"><input type="password" value={f.current_password} onChange={e => setF({ ...f, current_password: e.target.value })} autoComplete="current-password" /></Field>
        <Field label="New password" help="At least 8 characters."><input type="password" value={f.new_password} onChange={e => setF({ ...f, new_password: e.target.value })} autoComplete="new-password" /></Field>
      </div></Section>}
      <div className="row"><button type="submit" className="btn primary" disabled={busy}>Save changes</button></div>
    </form>
  )
}

function Tokens() {
  const { toast } = useToast()
  const { data, reload } = useAsync(() => api.get<APIToken[]>(`${V1}/tokens`), [])
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState('')
  const [del, setDel] = useState<APIToken | null>(null)
  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    try { const r = await api.post<{ token: string }>(`${V1}/tokens`, { name: name || 'token' }); setFresh(r.token); setName(''); reload() } catch (e) { toast((e as Error).message, 'err') }
  }
  return (
    <Section title="Personal tokens" icon="key" sub={<>Use as <code>Authorization: Bearer &lt;token&gt;</code>. A token acts as you and sees your reviews. Good for a Homepage widget (<code>GET /api/v1/dashboard</code>) or for uploading from a script. See <code>docs/api.md</code>.</>}>
      <form onSubmit={create} className="row mb"><input value={name} onChange={e => setName(e.target.value)} placeholder="Token name (e.g. Homepage)" aria-label="Token name" /><button type="submit" className="btn primary"><Icon name="plus" size={15} />Create</button></form>
      {fresh && <div className="set-fresh mb"><div className="row"><Icon name="check-circle" size={16} /><b>Copy it now — it is shown once.</b></div><pre className="mono">{fresh}</pre></div>}
      {data && data.length > 0 ? (
        <div className="table-wrap"><table className="table compact">
          <thead><tr><th>Name</th><th>Prefix</th><th>Last used</th><th>Created</th><th /></tr></thead>
          <tbody>{data.map(t => <tr key={t.id}><td>{t.name}</td><td className="mono">{t.prefix}…</td><td className="muted">{t.last_used_at ? fmtDateTime(t.last_used_at) : 'never'}</td><td className="muted">{fmtDateTime(t.created_at)}</td><td className="right"><button type="button" className="btn ghost sm danger-text" onClick={() => setDel(t)}>Revoke</button></td></tr>)}</tbody>
        </table></div>
      ) : <p className="muted">No tokens yet.</p>}
      {del && <Confirm confirmLabel="Revoke" title={`Revoke “${del.name}”?`} message="Anything using this token stops working immediately." onCancel={() => setDel(null)} onConfirm={async () => { await api.del(`${V1}/tokens/${del.id}`); setDel(null); reload() }} />}
    </Section>
  )
}

function About({ onShortcuts }: { onShortcuts: () => void }) {
  const { data, error } = useAsync(() => api.get<Record<string, unknown>>(`${V1}/settings/system`), [])
  if (error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>
  if (!data) return <Loading />
  const counts = (data.counts || {}) as Record<string, number>
  const core = (data.core || {}) as { configured?: boolean; registered?: boolean; url?: string; error?: string }
  const tiles: [string, number | undefined, string][] = [['Reviews', counts.reviews, 'review'], ['Comments', counts.comments, 'message'], ['Tasks', counts.tasks, 'check-square'], ['People on the box', counts.users, 'users']]
  const rows: [string, React.ReactNode][] = [
    ['Version', <code key="v">{String(data.version)}</code>],
    ['Uptime', String(data.uptime)],
    ['Runtime', `${String(data.go)} · ${String(data.os)}/${String(data.arch)}`],
    ['Time zone', String(data.timezone)],
    ['Sign-in', String(data.auth_mode)],
    ['Pocket Core', core.configured ? `${core.url} · ${core.registered ? 'registered' : core.error || 'not registered yet'}` : 'not configured'],
    ['Database', <span key="db"><code>{String(data.db_path)}</code> · {fmtBytes(Number(data.db_bytes))}</span>],
    ['Documents', <span key="docs"><code>{String(data.files_dir)}</code> · up to {String(data.max_upload_mb)} MB each</span>],
    ['Review clock', `tick ${String(data.tick)} · idle after ${String(data.idle_after)} · new session after ${String(data.session_gap)}`],
    ['Live now', `${String(data.live_connections)} open connection(s)`],
  ]
  return (
    <div className="stack loose">
      <div className="set-about-hero">
        <img src="/favicon.svg" alt="" width={48} height={48} />
        <div>
          <h3>Pocket Review</h3>
          <p className="muted small">Document review for small teams: comments pinned to the page, issues that get resolved or rejected, tasks, sign-off, live presence, and a report of who spent how long on what. One binary, one SQLite file plus the documents — part of the Pocket apps. PDF rendering by Mozilla pdf.js.</p>
        </div>
      </div>
      <div className="set-stats">
        {tiles.map(([label, n, icon]) => (
          <div key={label} className="set-stat"><Icon name={icon} size={16} /><span className="set-stat-n tnum">{n ?? 0}</span><span className="set-stat-l">{label}</span></div>
        ))}
      </div>
      <Section title="This server">
        <dl className="set-dl">
          {rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </Section>
      <div className="row wrap">
        <button type="button" className="btn" onClick={onShortcuts}><Icon name="keyboard" size={15} />Keyboard shortcuts</button>
        <a className="btn" href="https://github.com/abjelosevic88/pocket-homelab-review" target="_blank" rel="noreferrer"><Icon name="external" size={15} />Project on GitHub (MIT)</a>
      </div>
    </div>
  )
}
