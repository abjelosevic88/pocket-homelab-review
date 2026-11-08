// The app shell: sidebar, top bar, search palette, shortcuts — the Pocket Calendar layout.
import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { Link, Outlet, useLocation, useMatch, useNavigate, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { Dashboard, Review } from '../lib/types'
import { Icon } from './icons'
import { Avatar } from './people'
import { Sidebar } from './Sidebar'
import { CommandPalette, type PaletteCommand } from './CommandPalette'
import { ShortcutsSheet } from './ShortcutsSheet'
import { NewReview } from '../pages/Reviews'
import { type MenuItem, MenuButton, modalOpen, useIsMobile, useMediaQuery } from './ui'
import { AskButton } from './AskButton'
import { NotificationBell } from './Notifications'
import { pushRefresh } from '../lib/push'

const SIDEBAR_KEY = 'pcl-sidebar'

/** Something changed on a page: the sidebar list and counts reload. */
export const announceChange = () => window.dispatchEvent(new CustomEvent('pcl:changed'))
/** Open the "New review" dialog from anywhere. */
export const openNewReview = () => window.dispatchEvent(new CustomEvent('pcl:new-review'))

/** The open review's title, for the breadcrumb in the top bar. */
const CrumbCtx = createContext<(t: string) => void>(() => {})
export function useCrumb(title: string | undefined) {
  const set = useContext(CrumbCtx)
  useEffect(() => { set(title || ''); return () => set('') }, [title, set])
}

export default function Layout() {
  const navigate = useNavigate()
  const loc = useLocation()
  const [params, setParams] = useSearchParams()
  const mobile = useIsMobile()
  const narrow = useMediaQuery('(max-width: 1099px)')
  const reviewMatch = useMatch('/reviews/:id/*')
  const onReview = !!reviewMatch && /^\d+$/.test(reviewMatch.params.id || '')
  const onDocument = onReview && !loc.pathname.endsWith('/report')
  const drawer = mobile || narrow
  const [sidebarOpen, setSidebarOpen] = useState(() => !drawer && localStorage.getItem(SIDEBAR_KEY) !== '0')
  useEffect(() => { setSidebarOpen(!drawer && localStorage.getItem(SIDEBAR_KEY) !== '0') }, [drawer])
  const drawerOpenedAt = useRef(0)
  const toggleSidebar = () => setSidebarOpen(o => { drawerOpenedAt.current = performance.now(); const n = !o; if (!drawer) localStorage.setItem(SIDEBAR_KEY, n ? '1' : '0'); return n })
  const closeDrawer = () => { if (drawer) setSidebarOpen(false) }
  useEffect(() => { document.querySelector('.content')?.scrollTo({ top: 0 }) }, [loc.pathname])
  // keep this browser's notification subscription on the box (it can change or be forgotten)
  useEffect(() => { pushRefresh() }, [])

  const [palette, setPalette] = useState(false)
  const [shortcuts, setShortcuts] = useState(false)
  const [creating, setCreating] = useState(false)
  const [crumb, setCrumb] = useState('')

  // sidebar data: the open reviews and the "waiting for you" counts
  const [reviews, setReviews] = useState<Review[]>([])
  const [dash, setDash] = useState<Dashboard | null>(null)
  const reload = useCallback(() => {
    api.get<Review[]>(`${V1}/reviews?status=open&role=&q=`).then(r => setReviews(r || [])).catch(() => {})
    api.get<Dashboard>(`${V1}/dashboard`).then(setDash).catch(() => {})
  }, [])
  useEffect(() => { reload() }, [reload, loc.pathname])
  useEffect(() => {
    const t = setInterval(reload, 60000)
    window.addEventListener('pcl:changed', reload)
    return () => { clearInterval(t); window.removeEventListener('pcl:changed', reload) }
  }, [reload])

  // "New review": the sidebar button, n, the FAB, other pages, and the old /reviews?new=1 link
  useEffect(() => {
    const open = () => { if (drawer) setSidebarOpen(false); setCreating(true) }
    const sc = () => setShortcuts(true)
    window.addEventListener('pcl:new-review', open)
    window.addEventListener('pcl:shortcuts', sc)
    return () => { window.removeEventListener('pcl:new-review', open); window.removeEventListener('pcl:shortcuts', sc) }
  }, [drawer])
  useEffect(() => {
    if (params.get('new') === '1') { setCreating(true); setParams(p => { p.delete('new'); return p }, { replace: true }) }
  }, [params, setParams])

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette(p => !p); return }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (t.closest('input, textarea, select, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .popover, .sel-pop')) return
      const go = (to: string) => { e.preventDefault(); navigate(to) }
      switch (e.key) {
        case '/': e.preventDefault(); setPalette(true); break
        case '?': e.preventDefault(); setShortcuts(true); break
        case 'n': e.preventDefault(); setCreating(true); break
        case 'h': go('/home'); break
        case 'r': go('/reviews'); break
        case 't': go('/tasks'); break
        case ',': go('/settings'); break
        case '[': e.preventDefault(); toggleSidebar(); break
      }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }) // eslint-disable-line react-hooks/exhaustive-deps

  const commands: PaletteCommand[] = [
    { id: 'new', label: 'New review', icon: 'plus', hint: 'N', keywords: 'upload pdf document create start', run: () => setCreating(true) },
    { id: 'home', label: 'Home', icon: 'home', hint: 'H', keywords: 'dashboard waiting', run: () => navigate('/home') },
    { id: 'reviews', label: 'Reviews', icon: 'review', hint: 'R', keywords: 'documents list', run: () => navigate('/reviews') },
    { id: 'to-review', label: 'Waiting for my review', icon: 'message', keywords: 'reviewer todo', run: () => navigate('/reviews?role=reviewer') },
    { id: 'to-approve', label: 'Waiting for my decision', icon: 'thumbs-up', keywords: 'approver sign off decide', run: () => navigate('/reviews?role=approver') },
    { id: 'finished', label: 'Finished reviews', icon: 'archive', keywords: 'closed approved rejected done', run: () => navigate('/reviews?status=closed') },
    { id: 'tasks', label: 'Your tasks', icon: 'check-square', hint: 'T', keywords: 'todo assigned', run: () => navigate('/tasks') },
    { id: 'settings', label: 'Settings', icon: 'settings', hint: ',', run: () => navigate('/settings') },
    { id: 'notifications', label: 'Notification settings', icon: 'bell', keywords: 'push phone mention alerts', run: () => navigate('/settings/notifications') },
    { id: 'profile', label: 'Profile & password', icon: 'user', keywords: 'name email password account', run: () => navigate('/settings/profile') },
    { id: 'tokens', label: 'API tokens', icon: 'key', keywords: 'bearer homepage script', run: () => navigate('/settings/tokens') },
    { id: 'shortcuts', label: 'Keyboard shortcuts', icon: 'keyboard', hint: '?', run: () => setShortcuts(true) },
  ]

  return (
    <CrumbCtx.Provider value={setCrumb}>
      <div className={`app ${sidebarOpen ? 'with-sidebar' : 'no-sidebar'} ${mobile ? 'is-mobile' : ''} ${drawer ? 'drawer-mode' : ''}`}>
        {drawer && sidebarOpen && <div className="drawer-backdrop" onClick={e => { if (e.timeStamp - drawerOpenedAt.current > 400) setSidebarOpen(false) }} />}
        <div className={`sidebar-wrap ${sidebarOpen ? 'open' : ''}`}>
          <Sidebar reviews={reviews} dash={dash} onNavigate={closeDrawer} onNew={() => { closeDrawer(); setCreating(true) }} />
        </div>
        <div className="main">
          <TopBar onMenu={toggleSidebar} onPalette={() => setPalette(true)} mobile={mobile} compact={narrow} sidebarOpen={sidebarOpen} crumb={crumb} />
          <main className={`content ${onDocument ? 'wide' : ''}`}><div className="content-inner"><Outlet /></div></main>
        </div>
        {mobile && !onReview && <button type="button" className="fab" aria-label="New review" title="New review" onClick={() => setCreating(true)}><Icon name="plus" size={26} strokeWidth={2.2} /></button>}
        {palette && <CommandPalette onClose={() => setPalette(false)} commands={commands} reviews={reviews} />}
        {shortcuts && <ShortcutsSheet onClose={() => setShortcuts(false)} />}
        {creating && <NewReview onClose={() => setCreating(false)} />}
      </div>
    </CrumbCtx.Provider>
  )
}

const SECTION: Record<string, string> = { home: 'Home', reviews: 'Reviews', tasks: 'Your tasks' }

function TopBar({ onMenu, onPalette, mobile, compact, sidebarOpen, crumb }: { onMenu: () => void; onPalette: () => void; mobile: boolean; compact: boolean; sidebarOpen: boolean; crumb: string }) {
  const { user, themeMode, setThemeMode, status, setUser } = useApp()
  const navigate = useNavigate()
  const loc = useLocation()
  const parts = loc.pathname.split('/').filter(Boolean)
  const section = SECTION[parts[0] || 'home'] || ''
  const reviewId = parts[0] === 'reviews' && /^\d+$/.test(parts[1] || '') ? parts[1] : ''
  const onReport = !!reviewId && parts[2] === 'report'
  const signOut = async () => { await api.post(`${V1}/auth/logout`).catch(() => {}); setUser(null); navigate('/login') }
  const avatarMenu = (): MenuItem[] => [
    { heading: user?.name || user?.email || '' },
    { label: 'Profile', icon: 'user', onSelect: () => navigate('/settings/profile') },
    { label: 'Settings', icon: 'settings', onSelect: () => navigate('/settings'), hint: ',' },
    { label: 'Keyboard shortcuts', icon: 'keyboard', onSelect: () => window.dispatchEvent(new CustomEvent('pcl:shortcuts')), hint: '?' },
    'sep',
    { heading: 'Appearance' },
    { label: 'System', checked: themeMode === 'system', onSelect: () => setThemeMode('system') },
    { label: 'Light', checked: themeMode === 'light', onSelect: () => setThemeMode('light') },
    { label: 'Dark', checked: themeMode === 'dark', onSelect: () => setThemeMode('dark') },
    ...(status?.auth_mode === 'local' ? ['sep' as const, { label: 'Sign out', icon: 'log-out', onSelect: signOut }] : []),
  ]
  return (
    <header className="topbar">
      <button type="button" className="btn ghost icon-only" onClick={onMenu} aria-label={sidebarOpen ? 'Hide the sidebar' : 'Show the sidebar'} title="Menu ([)"><Icon name={mobile || compact ? 'menu' : 'sidebar'} size={18} /></button>
      <h1 className="range-title">
        {reviewId ? <>
          {!mobile && <><button type="button" className="crumb" onClick={() => navigate('/reviews')}>Reviews</button><Icon name="chevron-right" size={16} className="crumb-sep" /></>}
          {onReport
            ? <><Link className="crumb ellipsis" to={`/reviews/${reviewId}`}>{crumb || 'Review'}</Link><Icon name="chevron-right" size={16} className="crumb-sep" /><span>Report</span></>
            : <span className="ellipsis">{crumb || 'Review'}</span>}
        </> : section}
      </h1>
      <span className="spacer" />
      <button type="button" className="btn ghost icon-only" onClick={onPalette} aria-label="Search" title="Search (⌘K or /)"><Icon name="search" size={18} /></button>
      <NotificationBell />
      <AskButton />
      <MenuButton label="Account" className="avatar-btn" align="right" width={220} items={avatarMenu}>
        {user ? <Avatar id={user.id} name={user.name || user.email} size="xl" title="" /> : <Icon name="user" size={18} />}
      </MenuButton>
    </header>
  )
}
