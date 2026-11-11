import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, upload, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { useLive } from '../lib/live'
import type { Anchor, Comment, CommentKind, Event, Participant, Review, Revision, Role, Severity, Task, ThreadStatus, Verdict } from '../lib/types'
import { ago, fmtBytes, fmtDate, fmtDateTime, fmtDuration, personName, plural, REVIEWER_VERDICT_LABEL, ROLE_HELP, ROLE_LABEL, VERDICT_LABEL } from '../lib/format'
import { Confirm, Field, Loading, MenuButton, Modal, Segmented, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { announceChange, useCrumb } from '../components/Layout'
import { Avatar, LiveStack } from '../components/people'
import PdfViewer, { KIND_ICON, Tool } from '../components/PdfViewer'
import Compare from '../components/Compare'
import { countPages, isPdf, PdfDrop, PersonPicker, RoleBadge, RoleSelect, SeverityBadge, StatusBadge, SourceChip } from '../components/review-bits'
import { Select } from '../components/Select'
import { AttachButton, AttachStrip, CommentImages, useImageAttach } from '../components/attachments'
import { MentionText, MentionTextarea } from '../components/mentions'
import { EVERYONE, parsePeopleSel, PeopleFilter, PeopleSel, PersonOption, personShown } from '../components/PeopleFilter'

type Tab = 'threads' | 'tasks' | 'people' | 'log'

export default function ReviewPage() {
  const { id } = useParams()
  const rid = Number(id)
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const { user } = useApp()
  const { toast } = useToast()
  const [review, setReview] = useState<Review | null>(null)
  const [threads, setThreads] = useState<Comment[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const [logTick, setLogTick] = useState(0)
  const [err, setErr] = useState('')
  const [tab, setTab] = useState<Tab>('threads')
  const [selected, setSelected] = useState<number | null>(null)
  const [jump, setJump] = useState<{ page: number; y: number; nonce: number; threadId?: number } | null>(null)
  const [pending, setPending] = useState<{ page: number; anchor: Anchor } | null>(null)
  const [tool, setTool] = useState<Tool>('select')
  const [page, setPage] = useState(1)
  const [showCursors, setShowCursors] = useState(() => localStorage.getItem('pcl-cursors') !== '0')
  const [modal, setModal] = useState<'' | 'verdict' | 'upload' | 'edit' | 'cancel' | 'delete'>('')
  const [taskFrom, setTaskFrom] = useState<Comment | null | undefined>(undefined)
  const [people, setPeopleState] = useState<PeopleSel>(() => parsePeopleSel(localStorage.getItem(`pcl-people-${rid}`)))
  const setPeople = (sel: PeopleSel) => {
    setPeopleState(sel)
    if (sel.mode === 'all') localStorage.removeItem(`pcl-people-${rid}`); else localStorage.setItem(`pcl-people-${rid}`, JSON.stringify(sel))
  }

  const loadReview = useCallback(() => api.get<Review>(`${V1}/reviews/${rid}`).then(setReview).catch(e => setErr((e as Error).message)), [rid])
  const loadThreads = useCallback(() => api.get<Comment[]>(`${V1}/reviews/${rid}/comments`).then(setThreads).catch(() => {}), [rid])
  const loadTasks = useCallback(() => api.get<Task[]>(`${V1}/reviews/${rid}/tasks`).then(setTasks).catch(() => {}), [rid])
  useEffect(() => { loadReview(); loadThreads(); loadTasks() }, [loadReview, loadThreads, loadTasks])

  useCrumb(review?.title)

  const revId = Number(params.get('v')) || review?.current_revision_id || 0
  const rev = review?.revisions.find(v => v.id === revId)
  const isCurrent = revId === review?.current_revision_id

  const live = useLive(rid, revId, {
    onChanged: what => {
      if (what.includes('deleted')) { toast('This review was deleted', 'err'); navigate('/reviews'); return }
      if (what.includes('comments')) loadThreads()
      if (what.includes('tasks')) loadTasks()
      if (what.some(w => w === 'review' || w === 'people')) loadReview()
      if (what.includes('events')) setLogTick(t => t + 1)
    },
    onBye: () => { toast('You were taken off this review', 'err'); navigate('/reviews') },
  })
  const onPage = useCallback((p: number) => { setPage(p); live.setPage(p) }, [live.setPage]) // eslint-disable-line react-hooks/exhaustive-deps
  const onPageCount = useCallback((n: number) => {
    if (rev && rev.page_count === 0) api.put(`${V1}/reviews/${rid}/revisions/${rev.id}/pages`, { page_count: n }).catch(() => {})
  }, [rev, rid])

  // threads drawn on this revision: its own, plus open ones carried over from earlier revisions
  const { shown, carried } = useMemo(() => {
    const carried = new Set<number>()
    const shown = threads.filter(t => {
      if (t.revision_id === revId) return true
      const tRev = review?.revisions.find(v => v.id === t.revision_id)
      if (isCurrent && t.status === 'open' && tRev && rev && tRev.number < rev.number) { carried.add(t.id); return true }
      return false
    })
    return { shown, carried }
  }, [threads, revId, isCurrent, review, rev])
  // the people filter: everyone who is on the review or started a thread here, me first
  const personOptions = useMemo<PersonOption[]>(() => {
    const m = new Map<number, PersonOption>()
    for (const p of review?.participants ?? []) m.set(p.user_id, { id: p.user_id, name: personName(p), count: 0 })
    for (const t of shown) {
      const o = m.get(t.author_id) ?? { id: t.author_id, name: t.author_name || 'Someone', count: 0 }
      o.count++; m.set(t.author_id, o)
    }
    return [...m.values()].sort((a, b) => Number(b.id === user?.id) - Number(a.id === user?.id) || a.name.localeCompare(b.name))
  }, [review, shown, user])
  const visible = useMemo(() => shown.filter(t => personShown(people, t.author_id)), [shown, people])

  // ?c=<comment>: a notification points at a thread — show its revision, open it, and count it as read
  const wantComment = Number(params.get('c')) || 0
  useEffect(() => {
    if (!wantComment || !review || !threads.length) return
    const t = threads.find(x => x.id === wantComment || x.replies?.some(r => r.id === wantComment))
    if (t && !shown.some(x => x.id === t.id) && t.revision_id && t.revision_id !== revId && review.revisions.some(v => v.id === t.revision_id)) {
      setParams(p => { p.set('v', String(t.revision_id)); return p }, { replace: true })
      return
    }
    setParams(p => { p.delete('c'); return p }, { replace: true })
    api.post(`${V1}/notifications/read`, { review_id: rid, comment_id: t?.id ?? wantComment }).then(() => window.dispatchEvent(new CustomEvent('pcl:inbox'))).catch(() => {})
    if (!t) return
    setSelected(t.id); setTab('threads')
    if (!personShown(people, t.author_id)) setPeople(EVERYONE)
    setJump({ page: t.page || 1, y: t.anchor?.rects?.[0]?.y ?? 0, nonce: Date.now(), threadId: t.id })
  }, [wantComment, review, threads, shown, revId]) // eslint-disable-line react-hooks/exhaustive-deps

  if (err) return <div className="callout danger"><Icon name="alert" size={15} /><span>{err === 'not found' ? 'This review does not exist, or you are not on it.' : err} <Link to="/reviews">Back to reviews</Link></span></div>
  if (!review) return <Loading />

  const me = review.participants.find(p => p.user_id === user?.id)
  const role = review.my_role
  // a decided review (approved/rejected) still takes comments; tasks and revisions wait for a reopen
  const locked = ['approved', 'rejected', 'cancelled'].includes(review.status)
  const threadsLocked = review.status === 'cancelled'
  const canComment = role !== 'observer' && !threadsLocked
  const approvers = review.participants.filter(p => p.role === 'approver')
  const decider = role === 'approver' || (role === 'owner' && approvers.length === 0)

  const select = (t: Comment) => {
    setSelected(t.id); setTab('threads')
    if (!personShown(people, t.author_id)) setPeople(EVERYONE)
    const y = t.anchor?.rects?.[0]?.y ?? 0
    setJump({ page: t.page || 1, y, nonce: Date.now(), threadId: t.id })
  }
  const runStatus = async (action: string, note = '') => {
    try { setReview(await api.post<Review>(`${V1}/reviews/${rid}/status`, { action, note })); announceChange(); toast('Done') } catch (e) { toast((e as Error).message, 'err') }
  }
  const setRevision = (v: number) => setParams(p => { if (v === review.current_revision_id) p.delete('v'); else p.set('v', String(v)); return p })
  // compare: ?compare=<left revision>, the right side is the revision on screen (?v=)
  const compareId = review.revisions.some(v => v.id === Number(params.get('compare'))) ? Number(params.get('compare')) : 0
  const setCompare = (left: number, right: number) => setParams(p => {
    if (!left) p.delete('compare'); else p.set('compare', String(left))
    if (right === review.current_revision_id) p.delete('v'); else p.set('v', String(right))
    return p
  })
  const openCompare = () => {
    if (!rev) return
    const byNum = (n: number) => review.revisions.find(v => v.number === n)
    const older = byNum(rev.number - 1)
    if (older) setCompare(older.id, rev.id)
    else { const newer = byNum(rev.number + 1); if (newer) setCompare(rev.id, newer.id) }
  }

  return (
    <div className="review-page">
      <header className="review-head">
        <div className="grow" style={{ minWidth: 0 }}>
          <div className="row wrap" style={{ gap: 8 }}>
            <Link to="/reviews" className="btn ghost icon-only sm back-btn" title="All reviews" aria-label="All reviews"><Icon name="arrow-left" size={16} /></Link>
            <h1 className="review-title" title={review.title}>{review.title}</h1>
            <StatusBadge status={review.status} />
            <RoleBadge role={role} />
          </div>
          <div className="meta row wrap">
            {review.revisions.length > 1 && (compareId
              ? <button type="button" className="btn sm on" onClick={() => setCompare(0, revId)} title="Back to the single document"><Icon name="columns" size={14} />Comparing</button>
              : <button type="button" className="btn sm" onClick={openCompare} title="Two revisions side by side, with the changes marked"><Icon name="columns" size={14} />Compare</button>)}
            {review.revisions.length > 0 && !compareId && (
              <Select className="rev-select" value={String(revId)} onChange={v => setRevision(Number(v))} ariaLabel="Revision"
                options={[...review.revisions].reverse().map(v => ({ value: String(v.id), label: `v${v.number}${v.id === review.current_revision_id ? ' (current)' : ''}`, hint: `${fmtDate(v.created_at.slice(0, 10))} · ${v.filename}` }))} />
            )}
            {rev && <span>{rev.filename} · {fmtBytes(rev.size)}</span>}
            {review.due_date && <span>· due {fmtDate(review.due_date)}</span>}
            {review.open_issues > 0 && <span className="badge sev-major">{plural(review.open_issues, 'open issue')}</span>}
            {!isCurrent && <span className="badge warn">Viewing an older revision</span>}
            <SourceChip review={review} />
          </div>
        </div>
        <div className="head-live">
          <LiveStack people={live.people} meConn={live.me?.conn} onPick={p => { if (p.conn !== live.me?.conn) setJump({ page: p.page, y: 0, nonce: Date.now() }) }} />
          <span className={`conn ${live.connected ? 'on' : ''}`} title={live.connected ? 'Live' : 'Reconnecting…'} />
        </div>
        <div className="actions row wrap">
          <Actions review={review} me={me} decider={decider} onStatus={runStatus} openModal={setModal} />
          {rev && <a className="btn" href={`${V1}/reviews/${rid}/revisions/${rev.id}/file?download=1`} download title={`Download v${rev.number} (${rev.filename})`}><Icon name="download" size={15} />Download</a>}
          <Link className="btn" to={`/reviews/${rid}/report`}><Icon name="chart" size={15} />Report</Link>
        </div>
      </header>
      {review.description && <p className="review-desc">{review.description}</p>}

      {compareId > 0 && rev ? (
        <div className="review-body compare-mode">
          <Compare rid={rid} revisions={review.revisions} currentId={review.current_revision_id} leftId={compareId} rightId={rev.id} onPick={setCompare} onClose={() => setCompare(0, revId)} />
        </div>
      ) : (
      <div className="review-body">
        <div className="review-viewer">
          {rev ? (
            <PdfViewer url={`${V1}/reviews/${rid}/revisions/${rev.id}/file`} threads={visible} carried={carried} selectedId={selected}
              onSelect={tid => { const t = threads.find(x => x.id === tid); if (t) { setSelected(tid); setTab('threads') } }}
              tool={tool} setTool={setTool} canComment={canComment} pending={pending}
              onAnchor={(pg, a) => { setPending({ page: pg, anchor: a }); setTab('threads'); setSelected(null); setTool('select') }}
              onPage={onPage} onPageCount={onPageCount}
              cursors={Object.values(live.cursors).filter(c => c.conn !== live.me?.conn)} showCursors={showCursors}
              onCursor={live.sendCursor} jump={jump}
              selections={Object.values(live.selections).filter(s => s.conn !== live.me?.conn)} onSelection={live.sendSelection} />
          ) : (
            <div className="card pad no-doc"><Icon name="file" size={22} /><b>No document yet.</b> {role === 'owner' ? <button type="button" className="btn primary sm" onClick={() => setModal('upload')}><Icon name="upload" size={14} />Upload the PDF</button> : <span className="muted">The owner has not uploaded it yet.</span>}</div>
          )}
        </div>
        <aside className="review-side">
          <div className="tabs side-tabs" role="tablist">
            {([['threads', 'Threads', review.open_threads], ['tasks', 'Tasks', review.open_tasks], ['people', 'People', 0], ['log', 'Log', 0]] as [Tab, string, number][]).map(([t, l, n]) => (
              <button key={t} type="button" role="tab" aria-selected={tab === t} className={tab === t ? 'active' : ''} onClick={() => setTab(t)} title={l}>{l}{n > 0 && <span className="badge">{n}</span>}</button>
            ))}
          </div>
          <div className="side-body">
            {tab === 'threads' && <>
              {pending && <Composer rid={rid} revisionId={revId} people={review.participants} pending={pending} onCancel={() => setPending(null)}
                onSaved={c => { setPending(null); loadThreads(); loadReview(); setSelected(c.id) }} />}
              {!pending && canComment && <p className="muted small hint"><Icon name="info" size={14} /><span>Select text in the document and press <b>Comment</b>, or switch to <b>Select area</b> and drag a box. Type <b>@</b> to mention someone. {locked && `The review is ${review.status}; comments and replies are still welcome. `}{!isCurrent && 'You are on an older revision; new comments go on it.'}</span></p>}
              <Threads review={review} threads={visible} carried={carried} people={people} setPeople={setPeople} personOptions={personOptions} meId={user?.id} hidden={shown.length - visible.length} page={page} selected={selected} onSelect={select}
                reload={() => { loadThreads(); loadReview() }} onTask={c => setTaskFrom(c)} locked={threadsLocked} tasksLocked={locked} tasks={tasks} onOpenTasks={() => setTab('tasks')} />
            </>}
            {tab === 'tasks' && <Tasks review={review} tasks={tasks} threads={threads} locked={locked} onNew={() => setTaskFrom(null)} reload={() => { loadTasks(); loadReview() }} onOpenThread={t => select(t)} />}
            {tab === 'people' && <People review={review} live={live.people} reload={loadReview} />}
            {tab === 'log' && <Log rid={rid} tick={logTick} review={review} onOpenThread={cid => { const t = threads.find(x => x.id === cid || x.replies?.some(r => r.id === cid)); if (t) select(t) }} />}
          </div>
          <div className="side-foot row">
            <label className="row small muted"><input type="checkbox" checked={showCursors} onChange={e => { setShowCursors(e.target.checked); localStorage.setItem('pcl-cursors', e.target.checked ? '1' : '0') }} style={{ width: 'auto' }} /> Show others' cursors and selections</label>
            <span className="spacer" />
            {me && <span className="muted small" title="Measured while this document is open, visible and in use">Your time: {fmtDuration(me.active_seconds)}</span>}
          </div>
        </aside>
      </div>
      )}

      {modal === 'verdict' && <VerdictModal review={review} me={me!} decider={decider} onClose={() => setModal('')} onDone={r => { setReview(r); setModal('') }} />}
      {modal === 'upload' && <UploadModal review={review} onClose={() => setModal('')} onDone={() => { setModal(''); setParams(p => { p.delete('v'); return p }); loadReview() }} />}
      {modal === 'edit' && <EditModal review={review} onClose={() => setModal('')} onDone={() => { setModal(''); loadReview() }} onDelete={() => setModal('delete')} />}
      {modal === 'cancel' && <Confirm title="Cancel this review?" message="Nobody can comment any more. You can reopen it later; everything stays in the log." onCancel={() => setModal('')} onConfirm={() => { setModal(''); runStatus('cancel') }} />}
      {modal === 'delete' && <Confirm title="Delete this review for everyone?" message="The document, every revision, all comments, tasks, the log and the time records are deleted. This cannot be undone — cancel the review instead if you want to keep the record." onCancel={() => setModal('')}
        onConfirm={async () => { try { await api.del(`${V1}/reviews/${rid}`); toast('Review deleted'); navigate('/reviews') } catch (e) { toast((e as Error).message, 'err') } }} />}
      {taskFrom !== undefined && <TaskModal review={review} from={taskFrom} onClose={() => setTaskFrom(undefined)} onDone={() => { setTaskFrom(undefined); loadTasks(); loadReview(); setTab('tasks') }} />}
    </div>
  )
}

// ---------- header actions ----------

function Actions({ review, me, decider, onStatus, openModal }: { review: Review; me?: Participant; decider: boolean; onStatus: (a: string) => void; openModal: (m: 'verdict' | 'upload' | 'edit' | 'cancel') => void }) {
  const role = review.my_role
  const s = review.status
  const locked = ['approved', 'rejected', 'cancelled'].includes(s)
  const out: React.ReactNode[] = []
  if (role === 'owner') {
    if (s === 'draft') out.push(<button type="button" key="start" className="btn primary" disabled={!review.current_revision_id} onClick={() => onStatus('start')} title="Open the review to everyone on it">Start review</button>)
    if (s === 'changes_requested') {
      out.push(<button type="button" key="up" className="btn primary" onClick={() => openModal('upload')}><Icon name="upload" size={15} />Upload v{review.revisions.length + 1}</button>)
      out.push(<button type="button" key="resume" className="btn" onClick={() => onStatus('resume')} title="Back in review without a new revision">Resume</button>)
    }
    if (s === 'in_review' && decider) out.push(<button type="button" key="done" className="btn primary" onClick={() => openModal('verdict')}>Complete…</button>)
    if (locked) out.push(<button type="button" key="reopen" className="btn" onClick={() => onStatus('reopen')}>Reopen</button>)
  }
  if (s === 'in_review' && role === 'approver' && me) {
    out.push(<button type="button" key="decide" className={`btn ${me.verdict ? '' : 'primary'}`} onClick={() => openModal('verdict')}>{me.verdict ? `${VERDICT_LABEL[me.verdict as Exclude<Verdict, ''>]} · change` : 'Decide…'}</button>)
  }
  if (s === 'in_review' && role === 'reviewer' && me) {
    out.push(<button type="button" key="finish" className={`btn ${me.state === 'done' ? '' : 'primary'}`} onClick={() => openModal('verdict')}>{me.state === 'done' && me.verdict ? `${REVIEWER_VERDICT_LABEL[me.verdict as Exclude<Verdict, ''>]} · change` : 'Finish review…'}</button>)
  }
  if (role === 'owner') {
    out.push(
      <MenuButton key="more" label="More" className="btn icon-only" align="right" width={210} items={[
        ...(!locked ? [{ label: 'Upload new revision', icon: 'upload', onSelect: () => openModal('upload') }] : []),
        { label: 'Edit details…', icon: 'edit', onSelect: () => openModal('edit') },
        ...(!locked ? ['sep' as const, { label: 'Cancel review', icon: 'x-circle', danger: true, onSelect: () => openModal('cancel') }] : []),
      ]}><Icon name="more" size={16} /></MenuButton>,
    )
  }
  return <>{out}</>
}

// ---------- composer ----------

function Composer({ rid, revisionId, people, pending, onCancel, onSaved }: { rid: number; revisionId: number; people: Participant[]; pending: { page: number; anchor: Anchor }; onCancel: () => void; onSaved: (c: Comment) => void }) {
  const { toast } = useToast()
  const { user } = useApp()
  const [kind, setKind] = useState<CommentKind>('comment')
  const [severity, setSeverity] = useState<Severity>('major')
  const [body, setBody] = useState('')
  const [busy, setBusy] = useState(false)
  const att = useImageAttach(rid)
  const ref = useRef<HTMLTextAreaElement>(null)
  useEffect(() => { ref.current?.focus() }, [pending])
  const ready = (!!body.trim() || att.has) && !att.uploading
  const save = async () => {
    if (!ready) return
    setBusy(true)
    try {
      const c = await api.post<Comment>(`${V1}/reviews/${rid}/comments`, { revision_id: revisionId, page: pending.page, anchor: pending.anchor, kind, severity: kind === 'issue' ? severity : '', body, images: att.ids })
      onSaved(c)
    } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <div className="composer card">
      <div className="row muted small"><Icon name="message-plus" size={15} />New thread on page {pending.page}<span className="spacer" /><button type="button" className="btn ghost icon-only xs" onClick={onCancel} aria-label="Cancel"><Icon name="x" size={14} /></button></div>
      {pending.anchor.quote && <blockquote className="quote">{pending.anchor.quote}</blockquote>}
      <Segmented size="sm" value={kind} onChange={setKind} options={[{ value: 'comment', label: <><Icon name="message" size={13} />Comment</> }, { value: 'question', label: <><Icon name="help" size={13} />Question</> }, { value: 'issue', label: <><Icon name="alert" size={13} />Issue</> }]} />
      {kind === 'issue' && <Segmented size="sm" value={severity} onChange={setSeverity} options={[{ value: 'minor', label: 'Minor' }, { value: 'major', label: 'Major' }, { value: 'critical', label: 'Critical' }]} />}
      <MentionTextarea ref={ref} rows={3} value={body} onChange={setBody} people={people} meId={user?.id} placeholder={kind === 'issue' ? 'What is wrong, and what should it say? (@ to mention)' : kind === 'question' ? 'What would you like to know? (@ to mention)' : 'Your comment… (@ to mention, ⌘V to paste a picture)'}
        onPaste={att.onPaste} onDrop={att.onDrop}
        onKeyDown={e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) save(); if (e.key === 'Escape') onCancel() }} />
      <AttachStrip att={att} />
      <div className="row"><AttachButton att={att} /><span className="muted small"><kbd>⌘</kbd> <kbd>↵</kbd></span><span className="spacer" /><button type="button" className="btn" onClick={onCancel}>Cancel</button><button type="button" className="btn primary" onClick={save} disabled={busy || !ready}>Post</button></div>
    </div>
  )
}

// ---------- threads ----------

function Threads({ review, threads, carried, people, setPeople, personOptions, meId, hidden, page, selected, onSelect, reload, onTask, locked, tasksLocked, tasks, onOpenTasks }: {
  review: Review; threads: Comment[]; carried: Set<number>; page: number; selected: number | null; onSelect: (t: Comment) => void; reload: () => void; onTask: (c: Comment) => void; locked: boolean; tasksLocked: boolean
  tasks: Task[]; onOpenTasks: () => void
  people: PeopleSel; setPeople: (sel: PeopleSel) => void; personOptions: PersonOption[]; meId?: number; hidden: number
}) {
  const [status, setStatus] = useState<'open' | 'all' | 'resolved' | 'rejected'>('open')
  const [kind, setKind] = useState<'' | CommentKind>('')
  const [here, setHere] = useState(false)
  const list = threads
    .filter(t => status === 'all' || t.status === status)
    .filter(t => !kind || t.kind === kind)
    .filter(t => !here || t.page === page)
    .sort((a, b) => (a.page - b.page) || ((a.anchor?.rects?.[0]?.y ?? 0) - (b.anchor?.rects?.[0]?.y ?? 0)) || a.id - b.id)
  const selRef = useRef<HTMLDivElement>(null)
  useEffect(() => { selRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }) }, [selected])
  return (
    <>
      <div className="thread-filters">
        <PeopleFilter value={people} onChange={setPeople} people={personOptions} meId={meId} />
        <Select value={status} onChange={v => setStatus(v as typeof status)} ariaLabel="State"
          options={([['open', 'Open'], ['all', 'All'], ['resolved', 'Resolved'], ['rejected', 'Rejected']] as const).map(([v, l]) => ({ value: v, label: l, hint: String(threads.filter(t => v === 'all' || t.status === v).length) }))} />
        <Select value={kind} onChange={v => setKind(v as typeof kind)} ariaLabel="Kind"
          options={([['', 'Every kind'], ['issue', 'Issues'], ['question', 'Questions'], ['comment', 'Comments']] as const).map(([v, l]) => ({ value: v, label: l, hint: String(threads.filter(t => !v || t.kind === v).length) }))} />
        <label className="row small"><input type="checkbox" checked={here} onChange={e => setHere(e.target.checked)} style={{ width: 'auto' }} /> Page {page}</label>
      </div>
      {list.length === 0 && <p className="muted small" style={{ padding: '12px 2px' }}>{threads.length ? 'Nothing matches.' : hidden ? <>No threads from these people. <button type="button" className="linklike" onClick={() => setPeople(EVERYONE)}>Show everyone's</button></> : 'No threads yet.'}</p>}
      <div className="thread-list">
        {list.map(t => (
          <div key={t.id} ref={selected === t.id ? selRef : undefined}>
            <ThreadCard review={review} t={t} carried={carried.has(t.id)} open={selected === t.id} onSelect={() => onSelect(t)} reload={reload} onTask={() => onTask(t)} locked={locked} tasksLocked={tasksLocked} tasks={tasks.filter(x => x.comment_id === t.id)} onOpenTasks={onOpenTasks} />
          </div>
        ))}
      </div>
    </>
  )
}

function ThreadCard({ review, t, carried, open, onSelect, reload, onTask, locked, tasksLocked, tasks, onOpenTasks }: { review: Review; t: Comment; carried: boolean; open: boolean; onSelect: () => void; reload: () => void; onTask: () => void; locked: boolean; tasksLocked: boolean; tasks: Task[]; onOpenTasks: () => void }) {
  const { user } = useApp()
  const { toast } = useToast()
  const [reply, setReply] = useState('')
  const [closing, setClosing] = useState<ThreadStatus | null>(null)
  const [note, setNote] = useState('')
  const [editing, setEditing] = useState<Comment | null>(null)
  const [busy, setBusy] = useState(false)
  const att = useImageAttach(review.id)
  const role = review.my_role
  const mine = t.author_id === user?.id
  const canModerate = role === 'owner' || role === 'approver'
  const canClose = !locked && (canModerate || mine)
  const canReply = !locked && role !== 'observer'
  const revNum = review.revisions.find(v => v.id === t.revision_id)?.number
  const run = async (fn: () => Promise<unknown>, msg?: string) => { setBusy(true); try { await fn(); reload(); if (msg) toast(msg) } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) } }
  const setStatus = (status: ThreadStatus) => run(async () => { await api.post(`${V1}/reviews/${review.id}/comments/${t.id}/status`, { status, note }); setClosing(null); setNote('') },
    { open: 'Reopened', resolved: 'Resolved', rejected: 'Rejected' }[status])
  const del = (c: Comment) => window.confirm(c.parent_id ? 'Delete this reply?' : (t.replies?.length ? 'Delete this comment? The replies stay.' : 'Delete this thread?')) && run(() => api.del(`${V1}/reviews/${review.id}/comments/${c.id}`), 'Deleted')
  const canSend = (!!reply.trim() || att.has) && !att.uploading
  const send = () => canSend && run(async () => { await api.post(`${V1}/reviews/${review.id}/comments`, { parent_id: t.id, body: reply, images: att.ids }); setReply(''); att.clear() })
  return (
    <div className={`thread card ${open ? 'open' : ''} st-${t.status} kind-${t.kind} ${t.severity ? 'sev-' + t.severity : ''}`} title="Show in the document"
      onClick={e => { if ((e.target as HTMLElement).closest('button, a, textarea, input, .sel, .reply-box, .close-box, .thread-actions')) return; onSelect() }}>
      <div className="thread-head">
        <span className={`kind-ico ${t.kind} ${t.severity}`}>{KIND_ICON[t.kind]}</span>
        <span className="grow">
          <span className="row wrap" style={{ gap: 6 }}>
            <b>{t.author_name}</b>
            {t.kind === 'issue' && <SeverityBadge severity={t.severity} />}
            {t.kind === 'question' && <span className="badge">Question</span>}
            {t.status !== 'open' && <span className={`badge st-${t.status}`}><Icon name={t.status === 'resolved' ? 'check' : 'x'} size={11} strokeWidth={2.4} />{t.status === 'resolved' ? 'Resolved' : 'Rejected'}</span>}
            {carried && <span className="badge warn" title="Opened on an earlier revision and still open">from v{revNum}</span>}
          </span>
          <span className="muted small">#{t.id} · <button type="button" className="linklike" onClick={e => { e.stopPropagation(); onSelect() }}>page {t.page || '—'}</button> · {ago(t.created_at)}{t.edited_at && ' · edited'}</span>
        </span>
        {(t.replies?.length ?? 0) > 0 && !open && <span className="badge" title="Replies"><Icon name="reply" size={11} />{t.replies!.length}</span>}
        {mine && !locked && !t.deleted && editing?.id !== t.id && <OwnMenu onEdit={() => setEditing(t)} onDelete={() => del(t)} />}
      </div>
      {t.anchor?.quote && <blockquote className="quote">{t.anchor.quote}</blockquote>}
      {tasks.length > 0 && (
        <div className="thread-tasks">
          {tasks.map(k => (
            <button key={k.id} type="button" className={`task-chip ${k.status}`} onClick={onOpenTasks} title="Open in Tasks">
              <Icon name={k.status === 'done' ? 'check-square' : 'square'} size={14} /><span className="grow">{k.title}</span><span className="muted">{k.assignee_id ? `→ ${k.assignee_name}` : 'unassigned'}</span>
            </button>
          ))}
        </div>
      )}
      {editing?.id === t.id ? <EditBox review={review} c={t} onDone={() => { setEditing(null); reload() }} /> :
        <>{(t.body || t.deleted) && <div className={`body ${open ? '' : 'clamp'}`}>{t.deleted ? <i className="muted">deleted</i> : <MentionText text={t.body} people={review.participants} meId={user?.id} />}</div>}
          {!t.deleted && <CommentImages rid={review.id} ids={t.images} />}</>}
      {open && <>
        {t.replies?.map(r => (
          <div key={r.id} className="reply">
            <Avatar id={r.author_id} name={r.author_name} size="sm" />
            <div className="grow">
              <div className="small reply-head"><span className="grow"><b>{r.author_name}</b> <span className="muted">{ago(r.created_at)}{r.edited_at && ' · edited'}</span></span>
                {r.author_id === user?.id && !locked && !r.deleted && editing?.id !== r.id && <OwnMenu onEdit={() => setEditing(r)} onDelete={() => del(r)} />}</div>
              {editing?.id === r.id ? <EditBox review={review} c={r} onDone={() => { setEditing(null); reload() }} /> : <>
                {(r.body || r.deleted) && <div className="body">{r.deleted ? <i className="muted">deleted</i> : <MentionText text={r.body} people={review.participants} meId={user?.id} />}</div>}
                {!r.deleted && <CommentImages rid={review.id} ids={r.images} />}</>}
            </div>
          </div>
        ))}
        {t.status !== 'open' && <div className={`status-note st-${t.status}`}>{t.status === 'resolved' ? 'Resolved' : 'Rejected'} {t.status_at && ago(t.status_at)}{t.status_note && <>: {t.status_note}</>}</div>}
        {canReply && t.status === 'open' && (
          <div className="reply-box" onClick={e => e.stopPropagation()}>
            <MentionTextarea rows={2} value={reply} onChange={setReply} people={review.participants} meId={user?.id} placeholder="Reply… (@ to mention, ⌘V to paste a picture)" onPaste={att.onPaste} onDrop={att.onDrop} onKeyDown={e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) send() }} />
            <AttachStrip att={att} />
            <div className="row"><AttachButton att={att} /><span className="spacer" /><button type="button" className="btn primary sm" onClick={send} disabled={busy || !canSend}>Reply</button></div>
          </div>
        )}
        {closing && (
          <div className="close-box">
            <textarea rows={2} value={note} onChange={e => setNote(e.target.value)} autoFocus placeholder={closing === 'rejected' ? 'Why is this not going to change? (required)' : 'What was done? (optional)'} />
            <div className="row"><span className="spacer" /><button type="button" className="btn sm" onClick={() => setClosing(null)}>Cancel</button>
              <button type="button" className={`btn sm ${closing === 'rejected' ? 'danger' : 'primary'}`} disabled={busy || (closing === 'rejected' && !note.trim())} onClick={() => setStatus(closing)}>{closing === 'rejected' ? 'Reject' : 'Resolve'}</button></div>
          </div>
        )}
        {!closing && (
          <div className="thread-actions">
            {canClose && t.status === 'open' && <><button type="button" className="btn sm" onClick={() => setClosing('resolved')}><Icon name="check" size={14} />Resolve</button><button type="button" className="btn sm" onClick={() => setClosing('rejected')}><Icon name="x" size={14} />Reject</button></>}
            {canClose && t.status !== 'open' && <button type="button" className="btn sm" onClick={() => setStatus('open')}><Icon name="undo" size={14} />Reopen</button>}
            {canReply && !tasksLocked && <button type="button" className="btn ghost sm" onClick={onTask}><Icon name="check-square" size={14} />Make task</button>}
            <span className="spacer" />
            {mine && !locked && !t.deleted && <><button type="button" className="btn ghost sm" onClick={() => setEditing(t)}><Icon name="edit" size={14} />Edit</button><button type="button" className="btn ghost sm danger-text" onClick={() => del(t)}>Delete</button></>}
          </div>
        )}
      </>}
    </div>
  )
}

/** Edit / Delete on your own comment or reply. */
function OwnMenu({ onEdit, onDelete }: { onEdit: () => void; onDelete: () => void }) {
  return <MenuButton label="Edit or delete" className="btn ghost icon-only xs own-menu" align="right" width={150}
    items={[{ label: 'Edit', icon: 'edit', onSelect: onEdit }, { label: 'Delete', icon: 'trash', danger: true, onSelect: onDelete }]}><Icon name="more" size={15} /></MenuButton>
}

function EditBox({ review, c, onDone }: { review: Review; c: Comment; onDone: () => void }) {
  const { toast } = useToast()
  const { user } = useApp()
  const [body, setBody] = useState(c.body)
  const [kind, setKind] = useState<CommentKind>(c.kind)
  const [severity, setSeverity] = useState<Severity>(c.severity || 'major')
  const att = useImageAttach(review.id, c.images)
  const root = c.parent_id === 0
  const ready = (!!body.trim() || att.has) && !att.uploading
  const save = async () => {
    if (!ready) return
    try { await api.put(`${V1}/reviews/${review.id}/comments/${c.id}`, { body, images: att.ids, kind: root ? kind : undefined, severity: root && kind === 'issue' ? severity : '' }); onDone() } catch (e) { toast((e as Error).message, 'err') }
  }
  return (
    <div className="grid" style={{ gap: 6 }} onClick={e => e.stopPropagation()}>
      {root && <Segmented size="sm" value={kind} onChange={setKind} options={[{ value: 'comment', label: 'Comment' }, { value: 'question', label: 'Question' }, { value: 'issue', label: 'Issue' }]} />}
      {root && kind === 'issue' && <Segmented size="sm" value={severity} onChange={setSeverity} options={[{ value: 'minor', label: 'Minor' }, { value: 'major', label: 'Major' }, { value: 'critical', label: 'Critical' }]} />}
      <MentionTextarea rows={3} value={body} onChange={setBody} people={review.participants} meId={user?.id} onPaste={att.onPaste} onDrop={att.onDrop} autoFocus
        onKeyDown={e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) save(); if (e.key === 'Escape') { e.stopPropagation(); onDone() } }} />
      <AttachStrip att={att} />
      <div className="row"><AttachButton att={att} /><span className="spacer" /><button type="button" className="btn sm" onClick={onDone}>Cancel</button><button type="button" className="btn primary sm" onClick={save} disabled={!ready}>Save</button></div>
    </div>
  )
}

// ---------- tasks ----------

function Tasks({ review, tasks, threads, locked, onNew, reload, onOpenThread }: { review: Review; tasks: Task[]; threads: Comment[]; locked: boolean; onNew: () => void; reload: () => void; onOpenThread: (t: Comment) => void }) {
  const { user } = useApp()
  const { toast } = useToast()
  const canModerate = review.my_role === 'owner' || review.my_role === 'approver'
  const toggle = async (t: Task) => {
    try { await api.put(`${V1}/reviews/${review.id}/tasks/${t.id}`, { status: t.status === 'done' ? 'open' : 'done' }); reload() } catch (e) { toast((e as Error).message, 'err') }
  }
  const del = async (t: Task) => { try { await api.del(`${V1}/reviews/${review.id}/tasks/${t.id}`); reload() } catch (e) { toast((e as Error).message, 'err') } }
  return (
    <>
      {!locked && review.my_role !== 'observer' && <button type="button" className="btn sm mb-sm" onClick={onNew}><Icon name="plus" size={14} />Add task</button>}
      {tasks.length === 0 && <p className="muted small">No tasks. Turn a thread into one with <b>Make task</b>, or add one here.</p>}
      <ul className="task-list">
        {tasks.map(t => {
          const can = !locked && (canModerate || t.created_by === user?.id || t.assignee_id === user?.id)
          const th = threads.find(x => x.id === t.comment_id)
          const overdue = t.status === 'open' && t.due_date && t.due_date < new Date().toISOString().slice(0, 10)
          return (
            <li key={t.id} className={t.status}>
              <input type="checkbox" checked={t.status === 'done'} disabled={!can} onChange={() => toggle(t)} style={{ width: 'auto' }} aria-label="Done" />
              <div className="grow">
                <div className="t-title">{t.title}</div>
                <div className="muted small">
                  {t.assignee_id ? <>→ {t.assignee_name}</> : 'unassigned'}
                  {t.due_date && <> · <span className={overdue ? 'bad' : ''}>due {fmtDate(t.due_date)}</span></>}
                  {th && <> · <button type="button" className="linklike" onClick={() => onOpenThread(th)}>from #{th.id}</button></>}
                  {t.status === 'done' && t.done_at && <> · done {ago(t.done_at)}</>}
                </div>
                {t.notes && <div className="small">{t.notes}</div>}
              </div>
              {can && (canModerate || t.created_by === user?.id) && <button type="button" className="btn ghost icon-only xs danger-text" onClick={() => del(t)} title="Delete" aria-label="Delete task"><Icon name="trash" size={14} /></button>}
            </li>
          )
        })}
      </ul>
    </>
  )
}

function TaskModal({ review, from, onClose, onDone }: { review: Review; from: Comment | null; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const { user } = useApp()
  // whoever makes the task owns it until they hand it to someone
  const [f, setF] = useState({ title: from ? from.body.split('\n')[0].slice(0, 200) : '', notes: '', assignee_id: user?.id || 0, due_date: '' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { await api.post(`${V1}/reviews/${review.id}/tasks`, { ...f, comment_id: from?.id || 0 }); toast('Task added'); onDone() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={from ? `Task from #${from.id}` : 'New task'} onClose={onClose}>
      <form onSubmit={save} className="grid" style={{ gap: 12 }}>
        <Field label="Task"><input value={f.title} onChange={e => setF({ ...f, title: e.target.value })} required maxLength={300} autoFocus /></Field>
        <div className="two-col tight">
          <Field group label="Assignee"><Select value={f.assignee_id ? String(f.assignee_id) : ''} onChange={v => setF({ ...f, assignee_id: Number(v) || 0 })} placeholder="Nobody yet" clearLabel="Nobody yet" ariaLabel="Assignee"
            options={review.participants.map(p => ({ value: String(p.user_id), label: personName(p), hint: ROLE_LABEL[p.role] }))} /></Field>
          <Field label="Due"><input type="date" value={f.due_date} onChange={e => setF({ ...f, due_date: e.target.value })} /></Field>
        </div>
        <Field label="Notes" help="Optional"><textarea rows={2} value={f.notes} onChange={e => setF({ ...f, notes: e.target.value })} /></Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn primary" disabled={busy}>Add task</button></div>
      </form>
    </Modal>
  )
}

// ---------- people ----------

function People({ review, live, reload }: { review: Review; live: { user_id: number; active: boolean; page: number }[]; reload: () => void }) {
  const { user, users, reloadUsers } = useApp()
  const { toast } = useToast()
  const navigate = useNavigate()
  const [busy, setBusy] = useState(false)
  const owner = review.my_role === 'owner'
  const run = async (fn: () => Promise<unknown>, msg: string) => { setBusy(true); try { await fn(); reload(); toast(msg) } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) } }
  return (
    <>
      <ul className="people-list">
        {review.participants.map(p => {
          const here = live.find(l => l.user_id === p.user_id)
          return (
            <li key={p.user_id}>
              <span className="pos-rel"><Avatar id={p.user_id} name={personName(p)} size="lg" />{here && <span className={`here-dot ${here.active ? '' : 'idle'}`} title={here.active ? `Here now, page ${here.page}` : 'Here, idle'} />}</span>
              <div className="grow">
                <div className="row wrap" style={{ gap: 6 }}><b>{personName(p)}</b>{p.user_id === user?.id && <span className="badge">you</span>}</div>
                <div className="muted small">{p.email}</div>
                <div className="small">
                  {p.verdict ? <span className={`badge v-${p.verdict}`}>{p.role === 'reviewer' ? REVIEWER_VERDICT_LABEL[p.verdict as Exclude<Verdict, ''>] : VERDICT_LABEL[p.verdict as Exclude<Verdict, ''>]}</span>
                    : p.role !== 'owner' && p.role !== 'observer' && <span className="badge">{p.state === 'invited' ? 'Not opened yet' : 'Reviewing'}</span>}
                  <span className="muted"> · {fmtDuration(p.active_seconds)} · {p.last_seen_at ? `seen ${ago(p.last_seen_at)}` : 'never opened'}</span>
                </div>
                {p.verdict_note && <div className="small quote-inline">“{p.verdict_note}”</div>}
              </div>
              {owner && p.role !== 'owner' ? (
                <RoleSelect value={p.role} withOwner disabled={busy} onChange={role => {
                  if (role === p.role) return
                  if (role === 'owner' && !window.confirm(`Hand the review over to ${personName(p)}? You stay on as a reviewer.`)) return
                  run(() => api.put(`${V1}/reviews/${review.id}/participants/${p.user_id}`, { role }), 'Role changed')
                }} />
              ) : <RoleBadge role={p.role} />}
              {owner && p.role !== 'owner' && <button type="button" className="btn ghost icon-only sm danger-text" disabled={busy} onClick={() => window.confirm(`Take ${personName(p)} off this review?`) && run(() => api.del(`${V1}/reviews/${review.id}/participants/${p.user_id}`), 'Removed')} title="Remove" aria-label={`Remove ${personName(p)}`}><Icon name="x" size={15} /></button>}
            </li>
          )
        })}
      </ul>
      {owner && <div className="mt"><PersonPicker users={users} exclude={review.participants.map(p => p.user_id)} busy={busy}
        onAdd={inv => run(async () => { await api.post(`${V1}/reviews/${review.id}/participants`, inv); if (inv.email) await reloadUsers() }, 'Added')} /></div>}
      {review.my_role !== 'owner' && <button type="button" className="btn ghost sm danger-text mt" onClick={() => window.confirm('Leave this review? You will not see it any more.') && api.del(`${V1}/reviews/${review.id}/participants/${user!.id}`).then(() => navigate('/reviews'))}>Leave this review</button>}
      <p className="muted small mt">{review.require_resolved ? 'Approval needs every issue resolved or rejected.' : 'Approval is allowed with issues still open.'} {review.participants.some(p => p.role === 'approver') ? 'Every approver must approve.' : 'There is no approver: the owner completes the review.'}</p>
    </>
  )
}

// ---------- log ----------

const KIND_GROUPS = [['', 'Everything'], ['comment', 'Comments'], ['issue', 'Issues'], ['task', 'Tasks'], ['verdict', 'Verdicts'], ['review', 'Review'], ['participant', 'People'], ['revision', 'Revisions'], ['session', 'Sessions']]

function Log({ rid, tick, review, onOpenThread }: { rid: number; tick: number; review: Review; onOpenThread: (cid: number) => void }) {
  const [kind, setKind] = useState('')
  const [who, setWho] = useState(0)
  const [events, setEvents] = useState<Event[] | null>(null)
  useEffect(() => { api.get<Event[]>(`${V1}/reviews/${rid}/events?kind=${kind}&user=${who}&limit=500`).then(setEvents).catch(() => {}) }, [rid, kind, who, tick])
  return (
    <>
      <div className="thread-filters">
        <Select value={kind} onChange={setKind} ariaLabel="Kind" options={KIND_GROUPS.map(([k, l]) => ({ value: k, label: l }))} />
        <Select value={String(who)} onChange={v => setWho(Number(v))} ariaLabel="Who"
          options={[{ value: '0', label: 'Everyone' }, ...review.participants.map(p => ({ value: String(p.user_id), label: personName(p), hint: ROLE_LABEL[p.role] }))]} />
        <a className="btn ghost sm" href={`${V1}/reviews/${rid}/events.csv`} title="Download the whole log"><Icon name="download" size={14} />CSV</a>
      </div>
      {!events ? <Loading /> : events.length === 0 ? <p className="muted small">Nothing yet.</p> : (
        <ol className="log-list">
          {events.map(e => (
            <li key={e.id} className={`ev ev-${e.kind.split('.')[0]}`}>
              <Avatar id={e.user_id} name={e.user_name || '?'} size="sm" />
              <div className="grow">
                <div className="small"><b>{e.user_name || 'Someone'}</b> <span className="muted" title={fmtDateTime(e.created_at)}>{ago(e.created_at)}</span></div>
                <div className="small">{e.comment_id ? <button type="button" className="linklike" onClick={() => onOpenThread(e.comment_id)}>{e.summary}</button> : e.summary}</div>
              </div>
            </li>
          ))}
        </ol>
      )}
    </>
  )
}

// ---------- modals ----------

function VerdictModal({ review, me, decider, onClose, onDone }: { review: Review; me: Participant; decider: boolean; onClose: () => void; onDone: (r: Review) => void }) {
  const { toast } = useToast()
  const ownerCompletes = review.my_role === 'owner' // no approver on the review: the owner decides
  const [v, setV] = useState<Verdict>(me.verdict || 'approved')
  const [note, setNote] = useState(me.verdict_note || '')
  const [busy, setBusy] = useState(false)
  const blocked = decider && v === 'approved' && review.require_resolved && review.open_issues > 0
  const reviewers = review.participants.filter(p => p.role === 'reviewer')
  const pending = reviewers.filter(p => p.state !== 'done')
  const save = async (verdict: Verdict) => {
    setBusy(true)
    try {
      const r = await api.post<Review>(`${V1}/reviews/${review.id}/verdict`, { verdict, note })
      toast('Saved'); onDone(r)
    } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  const labels = decider ? VERDICT_LABEL : REVIEWER_VERDICT_LABEL
  return (
    <Modal title={decider ? (ownerCompletes ? 'Complete the review' : 'Your decision') : 'Finish your review'} onClose={onClose}>
      <div className="grid" style={{ gap: 14 }}>
        {decider && pending.length > 0 && <div className="callout">{plural(pending.length, 'reviewer')} {pending.length === 1 ? 'has' : 'have'} not finished yet: {pending.map(p => personName(p)).join(', ')}.</div>}
        <Segmented value={v} onChange={setV} options={(['approved', 'changes', 'rejected'] as const).map(x => ({ value: x, label: labels[x] }))} />
        {blocked && <div className="callout danger">{plural(review.open_issues, 'issue')} still open. This review only approves once every issue is resolved or rejected.</div>}
        <Field label="Note" help={decider ? 'Shown in the log and the report.' : 'Optional — what the owner should know.'}><textarea rows={3} value={note} onChange={e => setNote(e.target.value)} /></Field>
        <div className="form-actions">
          {me.verdict && <button type="button" className="btn ghost" onClick={() => save('')} disabled={busy}>Withdraw</button>}
          <span className="spacer" />
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button type="button" className={`btn ${v === 'rejected' ? 'danger' : 'primary'}`} disabled={busy || blocked} onClick={() => save(v)}>{labels[v as Exclude<Verdict, ''>]}</button>
        </div>
        {decider && <p className="muted small">{v === 'approved' ? (review.participants.filter(p => p.role === 'approver').length > 1 ? 'The review is approved once every approver approves.' : 'The review becomes approved.') : v === 'changes' ? 'The owner uploads a new revision; everyone looks again.' : 'The review ends as rejected.'}</p>}
      </div>
    </Modal>
  )
}

function UploadModal({ review, onClose, onDone }: { review: Review; onClose: () => void; onDone: () => void }) {
  const { toast } = useToast()
  const [file, setFile] = useState<File | null>(null)
  const [pages, setPages] = useState(0)
  const [note, setNote] = useState('')
  const [progress, setProgress] = useState(0)
  const [busy, setBusy] = useState(false)
  const next = review.revisions.length + 1
  const current = review.revisions.find(v => v.id === review.current_revision_id)
  const pick = async (f: File) => {
    if (!isPdf(f)) { toast('Only PDF documents for now', 'err'); return }
    setFile(f)
    try { setPages(await countPages(f)) } catch { setPages(0); toast('That PDF could not be read — is it damaged or password-protected?', 'err') }
  }
  const save = async () => {
    if (!file) return
    setBusy(true)
    try { await upload<Revision>(`${V1}/reviews/${review.id}/revisions`, { file, page_count: String(pages), note }, setProgress); toast(`v${next} uploaded`); onDone() } catch (e) { toast((e as Error).message, 'err'); setBusy(false) }
  }
  return (
    <Modal title={next === 1 ? 'Upload the document' : `Upload v${next}`} onClose={onClose}>
      <div className="grid" style={{ gap: 14 }}>
        <PdfDrop file={file} pages={pages} onPick={pick} disabled={busy}
          hint={current ? `or click to choose one · replaces v${current.number} (${current.filename})` : 'or click to choose one'} />
        {next > 1 && <Field label="What changed?" help="Shown in the log."><input value={note} onChange={e => setNote(e.target.value)} placeholder="e.g. Net 45, new liability cap" /></Field>}
        {next > 1 && <p className="muted small">Open threads carry over to the new revision. Everyone's verdict is cleared so they look again{review.status === 'changes_requested' ? ', and the review goes back in review' : ''}.</p>}
        {busy && <div className="bar lg"><span className="fill" style={{ width: `${Math.round(progress * 100)}%` }} /></div>}
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="button" className="btn primary" disabled={!file || busy} onClick={save}>{busy ? `Uploading… ${Math.round(progress * 100)}%` : `Upload v${next}`}</button></div>
      </div>
    </Modal>
  )
}

function EditModal({ review, onClose, onDone, onDelete }: { review: Review; onClose: () => void; onDone: () => void; onDelete: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ title: review.title, description: review.description, due_date: review.due_date, require_resolved: review.require_resolved })
  const save = async (e: React.FormEvent) => { e.preventDefault(); try { await api.put(`${V1}/reviews/${review.id}`, f); toast('Saved'); onDone() } catch (e) { toast((e as Error).message, 'err') } }
  return (
    <Modal title="Review details" onClose={onClose}>
      <form onSubmit={save} className="grid" style={{ gap: 12 }}>
        <Field label="Title"><input value={f.title} onChange={e => setF({ ...f, title: e.target.value })} required maxLength={200} /></Field>
        <Field label="Description"><textarea rows={3} value={f.description} onChange={e => setF({ ...f, description: e.target.value })} /></Field>
        <Field label="Due date"><input type="date" value={f.due_date} onChange={e => setF({ ...f, due_date: e.target.value })} /></Field>
        <label className="row small"><input type="checkbox" checked={f.require_resolved} onChange={e => setF({ ...f, require_resolved: e.target.checked })} style={{ width: 'auto' }} /> Approve only when every issue is resolved or rejected</label>
        <div className="form-actions"><button type="button" className="btn ghost danger-text" onClick={onDelete}>Delete review…</button><span className="spacer" /><button type="button" className="btn" onClick={onClose}>Cancel</button><button type="submit" className="btn primary">Save</button></div>
      </form>
    </Modal>
  )
}
