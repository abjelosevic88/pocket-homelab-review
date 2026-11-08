import { useCallback, useEffect, useRef, useState } from 'react'
import type { Presence, Rect } from './types'

export interface RemoteCursor { conn: string; user_id: number; name: string; color: string; page: number; x: number; y: number; at: number }
export interface RemoteSelection { conn: string; user_id: number; name: string; color: string; page: number; rects: Rect[] }

interface Options {
  onChanged?: (what: string[], by: number) => void
  onBye?: () => void
}

const STATE_EVERY = 5000

/**
 * The live channel of one open document: presence, cursors, change notices,
 * and the activity signal the server measures review time from. The browser
 * only reports facts (which page is in view, whether the tab is visible,
 * whether there was input since the last report); the server keeps the clock.
 */
export function useLive(reviewId: number, revisionId: number, opts: Options) {
  const [people, setPeople] = useState<Presence[]>([])
  const [cursors, setCursors] = useState<Record<string, RemoteCursor>>({})
  const [selections, setSelections] = useState<Record<string, RemoteSelection>>({})
  const [me, setMe] = useState<{ conn: string; color: string } | null>(null)
  const lastSel = useRef('')
  const [connected, setConnected] = useState(false)
  const ws = useRef<WebSocket | null>(null)
  const page = useRef(1)
  const input = useRef(true)
  const lastCursor = useRef(0)
  const optsRef = useRef(opts)
  optsRef.current = opts

  const send = useCallback((m: object) => {
    const s = ws.current
    if (s && s.readyState === WebSocket.OPEN) s.send(JSON.stringify(m))
  }, [])
  const sendState = useCallback(() => {
    send({ t: 'state', page: page.current, visible: document.visibilityState === 'visible', input: input.current })
    input.current = false
  }, [send])

  useEffect(() => {
    if (!reviewId || !revisionId) return
    let stopped = false
    let retry = 1000
    let timer: ReturnType<typeof setTimeout> | undefined
    const connect = () => {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const s = new WebSocket(`${proto}//${location.host}/api/v1/reviews/${reviewId}/live?revision=${revisionId}&page=${page.current}`)
      ws.current = s
      s.onopen = () => { setConnected(true); retry = 1000; input.current = true; sendState() }
      s.onmessage = ev => {
        let m: Record<string, unknown>
        try { m = JSON.parse(ev.data) } catch { return }
        switch (m.t) {
          case 'hello': setMe({ conn: m.conn as string, color: m.color as string }); break
          case 'presence': {
            const ppl = m.people as Presence[]
            setPeople(ppl)
            // someone new may have joined: let them see what is selected here, drop selections of tabs that left
            if (lastSel.current) send(JSON.parse(lastSel.current))
            setSelections(cur => Object.fromEntries(Object.entries(cur).filter(([c]) => ppl.some(p => p.conn === c))))
            break
          }
          case 'selection': {
            const sel = m as unknown as RemoteSelection
            setSelections(cur => {
              const next = { ...cur }
              if (!sel.page || !sel.rects?.length) delete next[sel.conn]
              else next[sel.conn] = sel
              return next
            })
            break
          }
          case 'cursor': {
            const c = m as unknown as RemoteCursor
            setCursors(cur => {
              const next = { ...cur }
              if (!c.page) delete next[c.conn]
              else next[c.conn] = { ...c, at: Date.now() }
              return next
            })
            break
          }
          case 'changed': optsRef.current.onChanged?.(m.what as string[], m.by as number); break
          case 'bye': stopped = true; optsRef.current.onBye?.(); break
          case 'inbox': window.dispatchEvent(new CustomEvent('pcl:inbox')); break
        }
      }
      s.onclose = () => {
        setConnected(false)
        setPeople([])
        setCursors({})
        setSelections({})
        if (stopped) return
        timer = setTimeout(connect, retry)
        retry = Math.min(retry * 2, 30000)
      }
    }
    connect()

    // anything the person does counts as "still reviewing"
    const mark = () => { input.current = true }
    const evs = ['mousemove', 'keydown', 'wheel', 'touchstart', 'pointerdown', 'scroll']
    evs.forEach(e => window.addEventListener(e, mark, { passive: true, capture: true }))
    const vis = () => { if (document.visibilityState === 'visible') input.current = true; sendState() }
    document.addEventListener('visibilitychange', vis)
    const iv = setInterval(sendState, STATE_EVERY)
    // cursors that stopped moving fade out
    const sweep = setInterval(() => setCursors(cur => {
      const now = Date.now()
      const stale = Object.values(cur).filter(c => now - c.at > 30000)
      if (!stale.length) return cur
      const next = { ...cur }
      stale.forEach(c => delete next[c.conn])
      return next
    }), 5000)

    return () => {
      stopped = true
      clearTimeout(timer)
      clearInterval(iv)
      clearInterval(sweep)
      evs.forEach(e => window.removeEventListener(e, mark, { capture: true }))
      document.removeEventListener('visibilitychange', vis)
      ws.current?.close()
      ws.current = null
    }
  }, [reviewId, revisionId, sendState])

  const setPage = useCallback((p: number) => {
    if (p === page.current) return
    page.current = p
    input.current = true
    sendState()
  }, [sendState])

  const sendCursor = useCallback((p: number, x: number, y: number) => {
    const now = Date.now()
    if (p !== 0 && now - lastCursor.current < 40) return
    lastCursor.current = now
    send({ t: 'cursor', page: p, x, y })
  }, [send])

  /** Share the text selected here (page 0 / no rects clears it). Sent only when it changes. */
  const sendSelection = useCallback((p: number, rects: Rect[]) => {
    const msg = p && rects.length ? JSON.stringify({ t: 'selection', page: p, rects }) : ''
    if (msg === lastSel.current) return
    lastSel.current = msg
    send(msg ? JSON.parse(msg) : { t: 'selection', page: 0 })
  }, [send])

  return { people, cursors, selections, me, connected, setPage, sendCursor, sendSelection }
}
