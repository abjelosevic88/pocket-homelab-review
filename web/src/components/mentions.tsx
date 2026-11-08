// @mentions: a textarea that offers the people on the review after "@", and
// comment text with the mentions picked out. The server reads the same
// handles (internal/server/notify.go) to know whom to notify.
import { forwardRef, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { personName } from '../lib/format'
import { Avatar } from './people'

export type MentionPerson = { user_id: number; name: string; email: string }

type Handle = { text: string; ids: number[] }

const wordRune = (ch: string) => /[\p{L}\p{N}_]/u.test(ch)

/** The ways each person can be @mentioned, longest first (as on the server). */
function handles(people: MentionPerson[]): Handle[] {
  const by = new Map<string, number[]>()
  const first = new Map<string, number>()
  for (const p of people) { const f = p.name.toLowerCase().split(/\s+/).filter(Boolean)[0]; if (f) first.set(f, (first.get(f) || 0) + 1) }
  const add = (t: string, id: number) => {
    t = t.trim().toLowerCase()
    if (!t) return
    const ids = by.get(t) || []
    if (!ids.includes(id)) by.set(t, [...ids, id])
  }
  for (const p of people) {
    add(p.name, p.user_id)
    const f = p.name.toLowerCase().split(/\s+/).filter(Boolean)
    if (f.length > 1 && first.get(f[0]) === 1) add(f[0], p.user_id)
    const at = p.email.indexOf('@')
    if (at > 0) add(p.email.slice(0, at), p.user_id)
  }
  const all = people.map(p => p.user_id)
  const out: Handle[] = [{ text: 'everyone', ids: all }, { text: 'all', ids: all }]
  for (const [text, ids] of by) if (text !== 'everyone' && text !== 'all') out.push({ text, ids })
  return out.sort((a, b) => b.text.length - a.text.length)
}

/** Comment text with each @mention in a chip; yours stands out. */
export function MentionText({ text, people, meId }: { text: string; people: MentionPerson[]; meId?: number }) {
  const hs = useMemo(() => handles(people), [people])
  if (!text.includes('@')) return <>{text}</>
  const low = text.toLowerCase()
  const parts: React.ReactNode[] = []
  let last = 0
  for (let i = 0; i < text.length; i++) {
    if (text[i] !== '@' || (i > 0 && (wordRune(text[i - 1]) || text[i - 1] === '.'))) continue
    const rest = low.slice(i + 1)
    const h = hs.find(h => rest.startsWith(h.text) && !(rest.length > h.text.length && wordRune(rest[h.text.length])))
    if (!h) continue
    const end = i + 1 + h.text.length
    if (i > last) parts.push(text.slice(last, i))
    const mine = !!meId && h.ids.includes(meId)
    const who = h.ids.length === 1 ? people.find(p => p.user_id === h.ids[0]) : undefined
    parts.push(<span key={i} className={`mention ${mine ? 'me' : ''}`} title={who ? `${personName(who)} · ${who.email}` : 'Everyone on the review'}>{text.slice(i, end)}</span>)
    last = end
    i = end - 1
  }
  if (last < text.length) parts.push(text.slice(last))
  return <>{parts}</>
}

type Props = Omit<React.TextareaHTMLAttributes<HTMLTextAreaElement>, 'value' | 'onChange'> & {
  value: string
  onChange: (v: string) => void
  people: MentionPerson[]
  meId?: number
}

const EVERYONE: MentionPerson = { user_id: -1, name: 'everyone', email: '' }

/** A textarea that lists the people on the review after "@" (↑ ↓ to move, ↵ or Tab to pick, Esc to close). */
export const MentionTextarea = forwardRef<HTMLTextAreaElement, Props>(function MentionTextarea({ value, onChange, people, meId, onKeyDown, ...rest }, fwd) {
  const ref = useRef<HTMLTextAreaElement>(null)
  useImperativeHandle(fwd, () => ref.current!, [])
  const [q, setQ] = useState<{ start: number; text: string } | null>(null)
  const [hi, setHi] = useState(0)

  // "@sa" right before the caret, after a space or at the start
  const look = (el: HTMLTextAreaElement) => {
    const upto = el.value.slice(0, el.selectionStart ?? 0)
    const m = /(^|[\s(\[{"'])@([\p{L}\p{N}_.-]{0,30})$/u.exec(upto)
    if (m) { setQ({ start: upto.length - m[2].length - 1, text: m[2].toLowerCase() }); setHi(0) } else setQ(null)
  }
  const options = useMemo(() => {
    if (!q) return []
    const others = people.filter(p => p.user_id !== meId)
    const match = (p: MentionPerson) => !q.text || p.name.toLowerCase().split(/\s+/).some(w => w.startsWith(q.text)) || p.name.toLowerCase().startsWith(q.text) || p.email.toLowerCase().startsWith(q.text)
    const list = others.filter(match)
    if (others.length > 1 && 'everyone'.startsWith(q.text)) list.push(EVERYONE)
    return list.slice(0, 8)
  }, [q, people, meId])

  const pick = (p: MentionPerson) => {
    const el = ref.current
    if (!el || !q) return
    const caret = el.selectionStart ?? value.length
    const ins = '@' + (p === EVERYONE ? 'everyone' : personName(p)) + ' '
    const next = value.slice(0, q.start) + ins + value.slice(caret)
    onChange(next)
    setQ(null)
    requestAnimationFrame(() => { el.focus(); const at = q.start + ins.length; el.setSelectionRange(at, at) })
  }

  return (
    <div className="mention-wrap">
      <textarea ref={ref} value={value} {...rest}
        onChange={e => { onChange(e.target.value); look(e.target) }}
        onClick={e => look(e.currentTarget)}
        onBlur={() => setTimeout(() => setQ(null), 150)}
        onKeyDown={e => {
          if (q && options.length) {
            if (e.key === 'ArrowDown') { e.preventDefault(); setHi(h => (h + 1) % options.length); return }
            if (e.key === 'ArrowUp') { e.preventDefault(); setHi(h => (h - 1 + options.length) % options.length); return }
            if ((e.key === 'Enter' && !e.metaKey && !e.ctrlKey) || e.key === 'Tab') { e.preventDefault(); pick(options[hi]); return }
            if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setQ(null); return }
          }
          onKeyDown?.(e)
        }} />
      {q && options.length > 0 && (
        <div className="mention-pop" role="listbox" aria-label="People on this review">
          {options.map((p, i) => (
            <button key={p.user_id} type="button" role="option" aria-selected={i === hi} className={`mention-opt ${i === hi ? 'hi' : ''}`}
              onMouseDown={e => { e.preventDefault(); pick(p) }} onMouseEnter={() => setHi(i)}>
              {p === EVERYONE
                ? <><span className="avatar sm everyone">@</span><span className="grow"><b>everyone</b><span className="muted small"> · tell all {people.length} on the review</span></span></>
                : <><Avatar id={p.user_id} name={personName(p)} size="sm" /><span className="grow ellipsis"><b>{personName(p)}</b> <span className="muted small">{p.email}</span></span></>}
            </button>
          ))}
        </div>
      )}
    </div>
  )
})
