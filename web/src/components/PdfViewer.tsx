import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import * as pdfjs from 'pdfjs-dist/legacy/build/pdf.mjs'
import type { PDFDocumentProxy } from 'pdfjs-dist'
import workerUrl from 'pdfjs-dist/legacy/build/pdf.worker.min.mjs?url'
import type { Anchor, Comment, Rect } from '../lib/types'
import type { RemoteCursor, RemoteSelection } from '../lib/live'
import { Icon } from './icons'

pdfjs.GlobalWorkerOptions.workerSrc = workerUrl

export type Tool = 'select' | 'comment'

/** Coloured boxes drawn over a page (the compare view's added/removed words). */
export interface Mark { rects: Rect[]; cls: string }
/** Where a viewer is scrolled: the line in the middle of it as page-1 + fraction, and the horizontal share. */
export interface ScrollPos { docY: number; fx: number }

interface Props {
  url: string
  threads?: Comment[]
  carried?: Set<number>
  selectedId?: number | null
  onSelect?: (id: number) => void
  tool?: Tool
  setTool?: (t: Tool) => void
  canComment?: boolean
  pending?: { page: number; anchor: Anchor } | null
  onAnchor?: (page: number, anchor: Anchor) => void
  onPage?: (page: number) => void
  onPageCount?: (n: number) => void
  cursors?: RemoteCursor[]
  showCursors?: boolean
  onCursor?: (page: number, x: number, y: number) => void
  selections?: RemoteSelection[]
  onSelection?: (page: number, rects: Rect[]) => void
  jump?: { page: number; y: number; nonce: number; threadId?: number } | null
  // the compare view: a zoom shared by both sides, marks, the loaded document, scroll sync
  zoom?: number
  onZoom?: (z: number) => void
  marks?: Map<number, Mark[]>
  onDoc?: (doc: PDFDocumentProxy | null) => void
  onScrollPos?: (pos: ScrollPos) => void
  follow?: (ScrollPos & { nonce: number }) | null
  title?: React.ReactNode
}

interface Size { w: number; h: number }

const ZOOMS = [0.5, 0.67, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3]
const MIN_ZOOM = 0.25, MAX_ZOOM = 5
const NONE: never[] = []
const NO_IDS = new Set<number>()
const noop = () => {}
const clampZoom = (z: number) => Math.max(MIN_ZOOM, Math.min(MAX_ZOOM, Math.round(z * 1000) / 1000))

export default function PdfViewer(props: Props) {
  const p = {
    threads: NONE as Comment[], carried: NO_IDS, selectedId: null, onSelect: noop, tool: 'select' as Tool, setTool: noop, canComment: false, pending: null,
    onAnchor: noop, onPage: noop, onPageCount: noop, cursors: NONE as RemoteCursor[], showCursors: false, onCursor: noop, selections: NONE as RemoteSelection[],
    onSelection: noop, jump: null, ...props,
  }
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null)
  const [sizes, setSizes] = useState<Size[]>([])
  const [error, setError] = useState('')
  const [progress, setProgress] = useState(0)
  const [ownZoom, setOwnZoom] = useState(1)
  const zoom = props.zoom ?? ownZoom
  const setZoom = useCallback((z: number) => { const v = clampZoom(z); if (props.onZoom) props.onZoom(v); else setOwnZoom(v) }, [props.onZoom]) // eslint-disable-line react-hooks/exhaustive-deps
  const zoomRef = useRef(zoom)
  zoomRef.current = zoom
  const [width, setWidth] = useState(0)
  const [near, setNear] = useState<Set<number>>(new Set([1, 2]))
  const [current, setCurrent] = useState(1)
  const [flash, setFlash] = useState<number | null>(null)
  const [selBtn, setSelBtn] = useState<{ x: number; y: number; page: number; anchor: Anchor } | null>(null)
  const scroller = useRef<HTMLDivElement>(null)
  const pageEls = useRef<Map<number, HTMLDivElement>>(new Map())
  const ratios = useRef<Map<number, number>>(new Map())
  const { onPage, onPageCount } = p
  const onDocRef = useRef(props.onDoc)
  onDocRef.current = props.onDoc

  // ---- load ----
  useEffect(() => {
    let dead = false
    setDoc(null); setSizes([]); setError(''); setProgress(0); onDocRef.current?.(null)
    const task = pdfjs.getDocument({ url: p.url, withCredentials: true })
    task.onProgress = ({ loaded, total }: { loaded: number; total: number }) => { if (total) setProgress(loaded / total) }
    task.promise.then(async d => {
      if (dead) return
      const out: Size[] = []
      for (let i = 1; i <= d.numPages; i++) {
        const pg = await d.getPage(i)
        const vp = pg.getViewport({ scale: 1 })
        out.push({ w: vp.width, h: vp.height })
      }
      if (dead) return
      setDoc(d); setSizes(out); onPageCount(d.numPages); onDocRef.current?.(d)
    }).catch(e => { if (!dead) setError((e as Error).message || 'Could not open the document') })
    return () => { dead = true; task.destroy() }
  }, [p.url]) // eslint-disable-line react-hooks/exhaustive-deps

  // ---- width → scale ----
  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const ro = new ResizeObserver(() => setWidth(el.clientWidth))
    ro.observe(el)
    setWidth(el.clientWidth)
    return () => ro.disconnect()
  }, [])
  const maxW = useMemo(() => sizes.reduce((m, s) => Math.max(m, s.w), 0) || 612, [sizes])
  const fit = width > 0 ? Math.min((width - 40) / maxW, 2.2) : 1
  const scale = Math.max(0.2, fit * zoom)

  // pages are laid out at the live scale at once; drawing them sharp waits until the zooming stops
  const [renderScale, setRenderScale] = useState(scale)
  useEffect(() => {
    if (renderScale === scale) return
    const t = setTimeout(() => setRenderScale(scale), 160)
    return () => clearTimeout(t)
  }, [scale, renderScale])

  // ---- zoom keeps a spot in place: the one under the pointer (wheel, pinch) or the middle ----
  const anchorAt = useCallback((cx: number, cy: number) => {
    const root = scroller.current
    if (!root) return null
    const x = root.scrollLeft + cx, y = root.scrollTop + cy
    let best: { n: number; el: HTMLDivElement } | null = null
    pageEls.current.forEach((el, n) => { if (el.offsetTop <= y + 8 && (!best || el.offsetTop > best.el.offsetTop)) best = { n, el } })
    const b = best as { n: number; el: HTMLDivElement } | null
    if (!b) return null
    return { page: b.n, fx: (x - b.el.offsetLeft) / b.el.offsetWidth, fy: (y - b.el.offsetTop) / b.el.offsetHeight, cx, cy }
  }, [])
  const pointerAnchor = useRef<ReturnType<typeof anchorAt>>(null)
  const middleAnchor = useRef<ReturnType<typeof anchorAt>>(null)
  const prevScale = useRef(scale)
  useLayoutEffect(() => {
    const root = scroller.current
    if (!root || prevScale.current === scale) return
    const a = pointerAnchor.current ?? middleAnchor.current
    pointerAnchor.current = null
    const ratio = scale / prevScale.current
    prevScale.current = scale
    const el = a && pageEls.current.get(a.page)
    if (!a || !el) { root.scrollTop = root.scrollTop * ratio; return }
    root.scrollTop = el.offsetTop + a.fy * el.offsetHeight - a.cy
    root.scrollLeft = el.offsetLeft + a.fx * el.offsetWidth - a.cx
  }, [scale])

  // Ctrl/⌘ + wheel and trackpad pinch zoom; a plain wheel scrolls
  useEffect(() => {
    const root = scroller.current
    if (!root) return
    const wheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return
      e.preventDefault()
      const r = root.getBoundingClientRect()
      pointerAnchor.current = anchorAt(e.clientX - r.left, e.clientY - r.top)
      const dy = e.deltaMode === 1 ? e.deltaY * 33 : e.deltaMode === 2 ? e.deltaY * 400 : e.deltaY
      setZoom(zoomRef.current * Math.exp(-Math.max(-120, Math.min(120, dy)) * 0.0022))
    }
    // Two-finger pinch on a phone or tablet zooms the document itself (redrawn
    // sharp once the fingers stop), not the browser's magnifier over the page.
    // The spot under the fingers stays under them, and moving both pans.
    let pinch: { dist: number; zoom: number; anchor: NonNullable<ReturnType<typeof anchorAt>> | null } | null = null
    let frame = 0
    const spread = (t: TouchList) => Math.hypot(t[0].clientX - t[1].clientX, t[0].clientY - t[1].clientY)
    const middle = (t: TouchList) => {
      const r = root.getBoundingClientRect()
      return { x: (t[0].clientX + t[1].clientX) / 2 - r.left, y: (t[0].clientY + t[1].clientY) / 2 - r.top }
    }
    const ts = (e: TouchEvent) => {
      if (e.touches.length !== 2) { pinch = null; return }
      e.preventDefault()
      const m = middle(e.touches)
      pinch = { dist: spread(e.touches) || 1, zoom: zoomRef.current, anchor: anchorAt(m.x, m.y) }
    }
    const tm = (e: TouchEvent) => {
      if (!pinch || e.touches.length !== 2) return
      e.preventDefault()
      const m = middle(e.touches)
      const next = pinch.zoom * spread(e.touches) / pinch.dist
      const a = pinch.anchor
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        if (a) pointerAnchor.current = { ...a, cx: m.x, cy: m.y }
        setZoom(next)
      })
    }
    const te = (e: TouchEvent) => { if (e.touches.length < 2) pinch = null }
    // Safari sends its own pinch events (trackpad on a Mac, fingers on an iPhone)
    let start = 1
    const gs = (e: Event) => { e.preventDefault(); start = zoomRef.current }
    const gc = (e: Event) => {
      e.preventDefault()
      if (pinch) return // the touch handlers above have it
      const g = e as Event & { scale: number; clientX: number; clientY: number }
      const r = root.getBoundingClientRect()
      pointerAnchor.current = anchorAt(g.clientX - r.left, g.clientY - r.top)
      setZoom(start * g.scale)
    }
    root.addEventListener('wheel', wheel, { passive: false })
    root.addEventListener('gesturestart', gs)
    root.addEventListener('gesturechange', gc)
    root.addEventListener('touchstart', ts, { passive: false })
    root.addEventListener('touchmove', tm, { passive: false })
    root.addEventListener('touchend', te)
    root.addEventListener('touchcancel', te)
    return () => {
      cancelAnimationFrame(frame)
      root.removeEventListener('wheel', wheel); root.removeEventListener('gesturestart', gs); root.removeEventListener('gesturechange', gc)
      root.removeEventListener('touchstart', ts); root.removeEventListener('touchmove', tm); root.removeEventListener('touchend', te); root.removeEventListener('touchcancel', te)
    }
  }, [anchorAt, setZoom])

  // ---- scroll position for the compare view (the line in the middle) ----
  const { onScrollPos, follow } = props
  const expectTop = useRef<number | null>(null)
  const posOf = useCallback((): ScrollPos | null => {
    const root = scroller.current
    if (!root || !pageEls.current.size) return null
    const y = root.scrollTop + root.clientHeight / 2
    let best: { n: number; el: HTMLDivElement } | null = null
    pageEls.current.forEach((el, n) => { if (el.offsetTop <= y && (!best || el.offsetTop > best.el.offsetTop)) best = { n, el } })
    const b = (best as { n: number; el: HTMLDivElement } | null) ?? { n: 1, el: pageEls.current.get(1)! }
    if (!b.el) return null
    const span = root.scrollWidth - root.clientWidth
    return { docY: b.n - 1 + Math.max(0, Math.min(1, (y - b.el.offsetTop) / b.el.offsetHeight)), fx: span > 0 ? root.scrollLeft / span : 0 }
  }, [])
  const onScroll = () => {
    if (selBtn) setSelBtn(null)
    const root = scroller.current
    if (!root) return
    middleAnchor.current = anchorAt(root.clientWidth / 2, root.clientHeight / 2)
    if (expectTop.current !== null && Math.abs(root.scrollTop - expectTop.current) < 2) { expectTop.current = null; return }
    expectTop.current = null
    if (onScrollPos) { const pos = posOf(); if (pos) onScrollPos(pos) }
  }
  useLayoutEffect(() => {
    const root = scroller.current
    if (!follow || !root || !sizes.length) return
    const n = Math.max(1, Math.min(sizes.length, Math.floor(follow.docY) + 1))
    const el = pageEls.current.get(n)
    if (!el) return
    const top = Math.round(Math.max(0, Math.min(root.scrollHeight - root.clientHeight, el.offsetTop + (follow.docY - (n - 1)) * el.offsetHeight - root.clientHeight / 2)))
    const span = root.scrollWidth - root.clientWidth
    const left = Math.round(follow.fx * span)
    if (Math.abs(root.scrollTop - top) >= 1) { expectTop.current = top; root.scrollTop = top }
    if (span > 0 && Math.abs(root.scrollLeft - left) >= 1) root.scrollLeft = left
  }, [follow]) // eslint-disable-line react-hooks/exhaustive-deps

  // ---- which pages are near (render) and which is current ----
  useEffect(() => {
    const root = scroller.current
    if (!root || !sizes.length) return
    const nearIO = new IntersectionObserver(entries => {
      setNear(prev => {
        const next = new Set(prev)
        for (const e of entries) {
          const n = Number((e.target as HTMLElement).dataset.page)
          if (e.isIntersecting) next.add(n); else next.delete(n)
        }
        return next
      })
    }, { root, rootMargin: '1200px 0px' })
    const visIO = new IntersectionObserver(entries => {
      for (const e of entries) ratios.current.set(Number((e.target as HTMLElement).dataset.page), e.intersectionRatio)
      let best = 1, bestR = -1
      ratios.current.forEach((r, n) => { if (r > bestR + 0.001 || (Math.abs(r - bestR) <= 0.001 && n < best)) { best = n; bestR = r } })
      if (bestR > 0) setCurrent(best)
    }, { root, threshold: [0, 0.1, 0.25, 0.5, 0.75, 0.9, 1] })
    pageEls.current.forEach(el => { nearIO.observe(el); visIO.observe(el) })
    return () => { nearIO.disconnect(); visIO.disconnect() }
  }, [sizes, renderScale])
  useEffect(() => { onPage(current) }, [current, onPage])

  // ---- jump to a thread ----
  useEffect(() => {
    if (!p.jump) return
    const el = pageEls.current.get(p.jump.page)
    const root = scroller.current
    if (!el || !root) return
    // put the spot in the upper third of the viewer; once there, pulse it so the eye finds it
    const target = Math.max(0, Math.min(root.scrollHeight - root.clientHeight, el.offsetTop + p.jump.y * el.offsetHeight - root.clientHeight / 3))
    const id = p.jump.threadId
    setFlash(null)
    let off: ReturnType<typeof setTimeout> | undefined
    const cancel = glide(root, target, () => {
      if (!id) return
      setFlash(id)
      off = setTimeout(() => setFlash(f => (f === id ? null : f)), 1600)
    })
    return () => { cancel(); clearTimeout(off) }
  }, [p.jump])

  const goPage = (n: number) => {
    const el = pageEls.current.get(Math.max(1, Math.min(sizes.length, n)))
    if (el && scroller.current) scroller.current.scrollTo({ top: el.offsetTop - 12 })
  }

  // ---- text selection → "Comment" button ----
  const { onSelection } = p
  const checkSelection = useCallback(() => {
    const sel = window.getSelection()
    if (!sel || sel.isCollapsed || !sel.rangeCount) { setSelBtn(null); onSelection(0, []); return }
    const range = sel.getRangeAt(0)
    const startEl = (range.startContainer.nodeType === 1 ? range.startContainer : range.startContainer.parentElement) as HTMLElement | null
    const pageEl = startEl?.closest('[data-page]') as HTMLElement | null
    if (!pageEl || !scroller.current?.contains(pageEl)) { setSelBtn(null); onSelection(0, []); return }
    const page = Number(pageEl.dataset.page)
    const box = pageEl.getBoundingClientRect()
    const rects: Rect[] = []
    for (const r of Array.from(range.getClientRects())) {
      if (r.width < 1 || r.height < 1) continue
      if (r.bottom < box.top || r.top > box.bottom) continue // another page
      const rect = { x: (r.left - box.left) / box.width, y: (r.top - box.top) / box.height, w: r.width / box.width, h: r.height / box.height }
      // pdf.js lays out one span per run: merge boxes on the same line
      const same = rects.find(o => Math.abs(o.y - rect.y) < 0.004 && Math.abs(o.h - rect.h) < 0.006 && rect.x <= o.x + o.w + 0.01 && rect.x + rect.w >= o.x - 0.01)
      if (same) {
        const x2 = Math.max(same.x + same.w, rect.x + rect.w)
        same.x = Math.min(same.x, rect.x); same.w = x2 - same.x
      } else rects.push(rect)
    }
    const quote = sel.toString().replace(/\s+/g, ' ').trim()
    if (!rects.length || !quote) { setSelBtn(null); onSelection(0, []); return }
    onSelection(page, rects.slice(0, 50))
    if (!p.canComment || p.tool !== 'select') { setSelBtn(null); return }
    const last = range.getClientRects()[range.getClientRects().length - 1]
    setSelBtn({ x: Math.min(last.right, window.innerWidth - 120), y: last.bottom + 6, page, anchor: { rects: rects.slice(0, 50), quote } })
  }, [p.canComment, p.tool, onSelection])
  useEffect(() => {
    const up = () => setTimeout(checkSelection, 0)
    document.addEventListener('mouseup', up)
    document.addEventListener('keyup', up)
    document.addEventListener('touchend', up)
    return () => { document.removeEventListener('mouseup', up); document.removeEventListener('keyup', up); document.removeEventListener('touchend', up) }
  }, [checkSelection])

  const threadsByPage = useMemo(() => {
    const m = new Map<number, Comment[]>()
    for (const t of p.threads) {
      if (!t.page) continue
      m.set(t.page, [...(m.get(t.page) || []), t])
    }
    return m
  }, [p.threads])

  const zoomBy = (dir: 1 | -1) => {
    const next = dir > 0 ? ZOOMS.find(z => z > zoom + 0.001) ?? MAX_ZOOM : [...ZOOMS].reverse().find(z => z < zoom - 0.001) ?? MIN_ZOOM
    setZoom(next)
  }

  return (
    <div className="viewer">
      <div className="viewer-bar">
        {props.title}
        {p.canComment && (
          <div className="segmented sm" role="radiogroup" aria-label="Tool">
            <button type="button" className={p.tool === 'select' ? 'active' : ''} onClick={() => p.setTool('select')} title="Select text, then comment on it"><Icon name="type" size={13} />Select text</button>
            <button type="button" className={p.tool === 'comment' ? 'active' : ''} onClick={() => p.setTool(p.tool === 'comment' ? 'select' : 'comment')} title="Drag a box (or click a point) on the page to comment on it"><Icon name="box-select" size={13} />Select area</button>
          </div>
        )}
        <span className="spacer" />
        {sizes.length > 0 && <span className="row page-ind">
          <button type="button" className="btn ghost sm" onClick={() => goPage(current - 1)} disabled={current <= 1} aria-label="Previous page"><Icon name="chevron-left" size={15} /></button>
          <input aria-label="Page" className="page-input" value={current} onChange={e => { const n = Number(e.target.value); if (n) goPage(n) }} />
          <span className="muted small">/ {sizes.length}</span>
          <button type="button" className="btn ghost sm" onClick={() => goPage(current + 1)} disabled={current >= sizes.length} aria-label="Next page"><Icon name="chevron-right" size={15} /></button>
        </span>}
        <span className="row zoom">
          <button type="button" className="btn ghost sm" onClick={() => zoomBy(-1)} aria-label="Zoom out" title="Zoom out"><Icon name="zoom-out" size={15} /></button>
          <button type="button" className="btn ghost sm zoom-val" onClick={() => setZoom(1)} title="Fit width — zoom with Ctrl/⌘ + scroll or a pinch">{Math.round(zoom * 100)}%</button>
          <button type="button" className="btn ghost sm" onClick={() => zoomBy(1)} aria-label="Zoom in" title="Zoom in"><Icon name="zoom-in" size={15} /></button>
        </span>
      </div>
      <div className={`viewer-scroll tool-${p.tool}`} ref={scroller} onScroll={onScroll} onPointerLeave={() => p.onCursor(0, 0, 0)}>
        {error && <div className="callout danger" style={{ margin: 24 }}>Could not open the document: {error}</div>}
        {!doc && !error && <div className="loading"><span className="spinner" /> Loading document… {progress > 0 && `${Math.round(progress * 100)}%`}</div>}
        {doc && sizes.map((s, i) => {
          const n = i + 1
          return (
            <PageView key={n} doc={doc} n={n} w={s.w * scale} h={s.h * scale} scale={scale} renderScale={renderScale} render={near.has(n)}
              register={el => { if (el) pageEls.current.set(n, el); else pageEls.current.delete(n) }}
              onMove={(x, y) => p.onCursor(n, x, y)}>
              <Overlay threads={threadsByPage.get(n) || []} carried={p.carried} selectedId={p.selectedId} flashId={flash} onSelect={p.onSelect}
                pending={p.pending?.page === n ? p.pending.anchor : null} />
              {props.marks?.get(n)?.map((m, i) => (
                <div key={'m' + i} className={`diff-mark ${m.cls}`}>{m.rects.map((r, j) => <span key={j} style={rectStyle(r)} />)}</div>
              ))}
              {p.tool === 'comment' && p.canComment && <DrawLayer onDone={a => p.onAnchor(n, a)} />}
              {p.showCursors && p.selections.filter(s => s.page === n).map(s => (
                <React.Fragment key={'sel' + s.conn}>
                  {s.rects.map((r, i) => <div key={i} className="remote-sel" style={{ ...rectStyle(r), background: s.color }} />)}
                  <span className="remote-sel-name" style={{ left: `${s.rects[0].x * 100}%`, top: `${s.rects[0].y * 100}%`, background: s.color }}>{s.name}</span>
                </React.Fragment>
              ))}
              {p.showCursors && p.cursors.filter(c => c.page === n).map(c => (
                <div key={c.conn} className="cursor" style={{ left: `${c.x * 100}%`, top: `${c.y * 100}%`, color: c.color }}>
                  <svg width="16" height="20" viewBox="0 0 16 20" aria-hidden><path d="M1 1l13 8.5-6 1.2-3 6.3z" fill="currentColor" stroke="#fff" strokeWidth="1.2" strokeLinejoin="round" /></svg>
                  <span style={{ background: c.color }}>{c.name}</span>
                </div>
              ))}
            </PageView>
          )
        })}
      </div>
      {selBtn && (
        <button type="button" className="btn primary sm sel-comment" style={{ left: selBtn.x, top: selBtn.y }}
          onMouseDown={e => e.preventDefault()}
          onClick={() => { p.onAnchor(selBtn.page, selBtn.anchor); setSelBtn(null); window.getSelection()?.removeAllRanges() }}>
          <Icon name="message" size={14} />Comment
        </button>
      )}
    </div>
  )
}

function PageView({ doc, n, w, h, scale, renderScale, render, register, onMove, children }: {
  doc: PDFDocumentProxy; n: number; w: number; h: number; scale: number; renderScale: number; render: boolean
  register: (el: HTMLDivElement | null) => void; onMove: (x: number, y: number) => void; children: React.ReactNode
}) {
  const canvas = useRef<HTMLCanvasElement>(null)
  const text = useRef<HTMLDivElement>(null)
  const [drawn, setDrawn] = useState(false)
  useEffect(() => {
    if (!render) {
      if (canvas.current) { canvas.current.width = 0; canvas.current.height = 0 }
      if (text.current) text.current.innerHTML = ''
      setDrawn(false)
      return
    }
    let dead = false
    let task: { cancel: () => void; promise: Promise<unknown> } | null = null
    let tl: pdfjs.TextLayer | null = null
    doc.getPage(n).then(page => {
      if (dead || !canvas.current || !text.current) return
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      const vp = page.getViewport({ scale: renderScale * dpr })
      const c = canvas.current
      c.width = Math.floor(vp.width); c.height = Math.floor(vp.height)
      task = page.render({ canvas: c, viewport: vp })
      task.promise.then(() => { if (!dead) setDrawn(true) }).catch(() => {})
      text.current.innerHTML = ''
      tl = new pdfjs.TextLayer({ textContentSource: page.streamTextContent(), container: text.current, viewport: page.getViewport({ scale: renderScale }) })
      tl.render().catch(() => {})
    })
    return () => { dead = true; task?.cancel(); tl?.cancel() }
  }, [doc, n, renderScale, render])
  const style = { width: w, height: h, '--total-scale-factor': scale, '--scale-round-x': '1px', '--scale-round-y': '1px' } as React.CSSProperties
  return (
    <div className="pdf-page" data-page={n} ref={register} style={style}
      onPointerMove={e => { const r = e.currentTarget.getBoundingClientRect(); onMove((e.clientX - r.left) / r.width, (e.clientY - r.top) / r.height) }}>
      <canvas ref={canvas} style={{ width: w, height: h, opacity: drawn ? 1 : 0 }} />
      <div className="textLayer" ref={text} />
      {children}
      <span className="page-no">{n}</span>
    </div>
  )
}

/** Drag a box, or click a point, to anchor a new thread. */
function DrawLayer({ onDone }: { onDone: (a: Anchor) => void }) {
  const [box, setBox] = useState<Rect | null>(null)
  const start = useRef<{ x: number; y: number } | null>(null)
  const pos = (e: React.PointerEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect()
    return { x: Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)), y: Math.max(0, Math.min(1, (e.clientY - r.top) / r.height)) }
  }
  return (
    <div className="draw-layer"
      onPointerDown={e => { e.currentTarget.setPointerCapture(e.pointerId); start.current = pos(e); setBox({ ...start.current, w: 0, h: 0 }) }}
      onPointerMove={e => {
        if (!start.current) return
        const q = pos(e), s = start.current
        setBox({ x: Math.min(s.x, q.x), y: Math.min(s.y, q.y), w: Math.abs(q.x - s.x), h: Math.abs(q.y - s.y) })
      }}
      onPointerUp={() => {
        const b = box
        start.current = null
        setBox(null)
        if (!b) return
        onDone({ rects: [b.w < 0.01 && b.h < 0.008 ? { x: b.x, y: b.y, w: 0, h: 0 } : b] })
      }}>
      {box && (box.w > 0 || box.h > 0) && <div className="anno pending" style={rectStyle(box)} />}
    </div>
  )
}

function rectStyle(r: Rect): React.CSSProperties {
  return { left: `${r.x * 100}%`, top: `${r.y * 100}%`, width: `${r.w * 100}%`, height: `${r.h * 100}%` }
}

function threadClass(t: Comment) {
  if (t.status !== 'open') return `done ${t.status}`
  if (t.kind === 'issue') return `issue ${t.severity}`
  return t.kind
}

/**
 * Eased scroll that stays smooth on slow machines: a long jump first snaps to
 * within one screen of the target (so the pages in between are not drawn
 * mid-animation), then glides the rest. Wheel/touch/keys by the reader stop
 * it. With "reduce motion" it jumps straight there.
 */
function glide(root: HTMLElement, target: number, done: () => void): () => void {
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) { root.scrollTop = target; done(); return () => {} }
  const near = root.clientHeight * 0.9
  if (Math.abs(target - root.scrollTop) > near * 2) root.scrollTop = target - Math.sign(target - root.scrollTop) * near
  const from = root.scrollTop, dist = target - from
  if (Math.abs(dist) < 2) { done(); return () => {} }
  const dur = Math.min(650, 280 + Math.abs(dist) * 0.35)
  const ease = (t: number) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2)
  let raf = 0, stopped = false
  const stop = () => { stopped = true; cancelAnimationFrame(raf); off() }
  const off = () => ['wheel', 'touchstart', 'keydown', 'mousedown'].forEach(e => root.removeEventListener(e, stop))
  ;['wheel', 'touchstart', 'keydown', 'mousedown'].forEach(e => root.addEventListener(e, stop, { passive: true }))
  const t0 = performance.now()
  const step = (now: number) => {
    if (stopped) return
    const t = Math.min(1, (now - t0) / dur)
    root.scrollTop = from + dist * ease(t)
    if (t < 1) raf = requestAnimationFrame(step)
    else { off(); done() }
  }
  raf = requestAnimationFrame(step)
  return stop
}

export const KIND_ICON: Record<string, string> = { issue: '!', question: '?', comment: '…' }

function Overlay({ threads, carried, selectedId, flashId, onSelect, pending }: {
  threads: Comment[]; carried: Set<number>; selectedId: number | null; flashId: number | null; onSelect: (id: number) => void; pending: Anchor | null
}) {
  return (
    <div className="overlay">
      {threads.map(t => {
        const rects = t.anchor?.rects?.length ? t.anchor.rects : [{ x: 0.94, y: 0.02, w: 0, h: 0 }]
        const first = rects[0]
        const point = first.w === 0 && first.h === 0
        const sel = selectedId === t.id
        const cls = `${threadClass(t)} ${sel ? 'picked' : ''} ${flashId === t.id ? 'flash' : ''} ${carried.has(t.id) ? 'carried' : ''}`
        const pinX = point ? first.x : Math.min(0.985, Math.max(...rects.map(r => r.x + r.w)) + 0.008)
        const pinY = first.y
        return (
          <React.Fragment key={t.id}>
            {!point && rects.map((r, i) => <div key={i} className={`anno ${cls}`} style={rectStyle(r)} />)}
            <button type="button" className={`pin ${cls}`} style={{ left: `${pinX * 100}%`, top: `${pinY * 100}%` }}
              onClick={e => { e.stopPropagation(); onSelect(t.id) }} title={`#${t.id} ${t.kind}${t.severity ? ` (${t.severity})` : ''} — ${t.author_name}: ${t.body.slice(0, 120)}`}>
              {t.status === 'open' ? KIND_ICON[t.kind] : t.status === 'resolved' ? '✓' : '✕'}
            </button>
          </React.Fragment>
        )
      })}
      {pending && pending.rects.map((r, i) => r.w === 0 && r.h === 0
        ? <span key={i} className="pin pending" style={{ left: `${r.x * 100}%`, top: `${r.y * 100}%` }}>+</span>
        : <div key={i} className="anno pending" style={rectStyle(r)} />)}
    </div>
  )
}
