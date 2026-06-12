import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'

// Lightweight, dependency-free autocomplete for the SQL surfaces (grid WHERE /
// ORDER BY inputs and the query console textarea). The owner supplies a pool of
// candidates (columns, table names, keywords) via `suggest`; this component
// detects the identifier under the caret, filters the pool by it, and renders a
// caret-anchored popup with keyboard navigation. Selecting an item replaces just
// that identifier, so the user keeps typing where they left off.

export interface Suggestion {
  label: string        // shown in the list and matched against the typed token
  insert?: string      // text inserted on accept (defaults to label)
  detail?: string      // dim right-aligned hint (e.g. a column's type)
  kind?: string        // small uppercase tag (column / table / keyword / …)
  caret?: number       // caret offset within `insert` (e.g. between quotes); default = end
}

// Quote an identifier only when it isn't a plain lowercase snake_case name, so
// inserted columns/tables stay un-noisy ("user_id") but odd names stay valid.
export const qIdent = (name: string) =>
  /^[a-z_][a-z0-9_]*$/.test(name) ? name : '"' + name.replace(/"/g, '""') + '"'

interface Props {
  as: 'input' | 'textarea'
  value: string
  onChange: (v: string) => void
  // Candidate pool, filtered here by the identifier under the caret. Pass a
  // stable (memoized) array so the recompute effect doesn't loop.
  candidates: Suggestion[]
  className?: string
  placeholder?: string
  // Forwarded to the field for keys the popup doesn't consume (form submit,
  // Ctrl+Enter to run, …). Not called while the popup handles the key.
  onKeyDown?: (e: React.KeyboardEvent) => void
  // Optional syntax highlighter (textarea only): given the current text, returns
  // an HTML string of coloured <span>s painted in an overlay behind a transparent
  // textarea. A string (not nodes) keeps each keystroke to one innerHTML write.
  highlight?: (v: string) => string
}

const TOKEN_RE = /[A-Za-z_][A-Za-z0-9_]*$/

// Rank prefix matches above interior matches, then alphabetically.
function filterPool(pool: Suggestion[], token: string): Suggestion[] {
  const q = token.toLowerCase()
  if (!q) return pool.slice(0, 50)
  const scored: Array<{ s: Suggestion; r: number }> = []
  for (const s of pool) {
    const i = s.label.toLowerCase().indexOf(q)
    if (i < 0) continue
    scored.push({ s, r: i === 0 ? 0 : 1 })
  }
  scored.sort((a, b) => a.r - b.r || a.s.label.localeCompare(b.s.label))
  return scored.slice(0, 50).map(x => x.s)
}

export default function CodeField({ as, value, onChange, candidates, className, placeholder, onKeyDown, highlight }: Props) {
  const ref = useRef<HTMLInputElement & HTMLTextAreaElement>(null)
  const hlRef = useRef<HTMLPreElement>(null)
  const hl = as === 'textarea' && !!highlight
  // Trailing newline keeps a blank last line in step with the textarea (which
  // always reserves one). Memoised so we only re-tokenise when the text changes.
  const hlHtml = useMemo(() => (hl ? highlight!(value) + '\n' : ''), [hl, highlight, value])
  const [caret, setCaret] = useState(0)
  const [forced, setForced] = useState(false) // Ctrl+Space opened the popup on an empty token
  const [items, setItems] = useState<Suggestion[]>([])
  const [active, setActive] = useState(0)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)
  const tokenStart = useRef(0)
  const pendingCaret = useRef<number | null>(null)
  const justAccepted = useRef(false) // don't reopen on the word we just inserted
  const open = pos !== null && items.length > 0

  // Recompute the token + candidate list whenever the text or caret moves.
  useEffect(() => {
    const el = ref.current
    if (!el) return
    if (justAccepted.current) { justAccepted.current = false; setPos(null); return }
    const before = value.slice(0, caret)
    const m = before.match(TOKEN_RE)
    const token = m ? m[0] : ''
    tokenStart.current = caret - token.length
    if (!token && !forced) { setPos(null); return }
    const list = filterPool(candidates, token)
    setItems(list)
    setActive(0)
    setPos(list.length ? caretXY(el, tokenStart.current, as === 'input') : null)
  }, [value, caret, forced, candidates, as])

  // Apply a caret position after a value change re-renders the controlled field.
  useLayoutEffect(() => {
    if (pendingCaret.current != null && ref.current) {
      ref.current.selectionStart = ref.current.selectionEnd = pendingCaret.current
      pendingCaret.current = null
    }
  })

  const sync = () => { const el = ref.current; if (el) setCaret(el.selectionStart ?? 0) }

  // Keep the highlight overlay aligned with the textarea's scroll position.
  const syncScroll = () => {
    const el = ref.current, pre = hlRef.current
    if (el && pre) { pre.scrollTop = el.scrollTop; pre.scrollLeft = el.scrollLeft }
  }
  useLayoutEffect(syncScroll)

  const accept = (s: Suggestion) => {
    const ins = s.insert ?? s.label
    const next = value.slice(0, tokenStart.current) + ins + value.slice(caret)
    const pos = tokenStart.current + (s.caret ?? ins.length)
    pendingCaret.current = pos
    justAccepted.current = true
    setForced(false)
    setPos(null)
    onChange(next)
    setCaret(pos)
  }

  const onKey = (e: React.KeyboardEvent) => {
    if (open && !e.ctrlKey && !e.metaKey) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setActive(a => (a + 1) % items.length); return }
      if (e.key === 'ArrowUp') { e.preventDefault(); setActive(a => (a - 1 + items.length) % items.length); return }
      if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); accept(items[active]); return }
      if (e.key === 'Escape') { e.preventDefault(); setForced(false); setPos(null); return }
    }
    // Ctrl+Space force-opens the popup even on whitespace / an empty token.
    if ((e.ctrlKey || e.metaKey) && e.key === ' ') { e.preventDefault(); setForced(true); sync(); return }
    onKeyDown?.(e)
  }

  const fieldProps = {
    ref,
    className,
    value,
    placeholder,
    onChange: (e: React.ChangeEvent<HTMLInputElement & HTMLTextAreaElement>) => {
      onChange(e.target.value); setCaret(e.target.selectionStart ?? 0)
    },
    onKeyDown: onKey,
    onKeyUp: sync,
    onClick: sync,
    onSelect: sync,
    onScroll: syncScroll,
    // Let an item's onMouseDown (which preventDefaults) run before we close.
    onBlur: () => { setForced(false); setPos(null) },
    spellCheck: false,
    autoComplete: 'off',
  }

  return (
    <span className={`ac-wrap ${as === 'textarea' ? 'area' : 'fill'}${hl ? ' hl' : ''}`}>
      {hl && (
        <pre ref={hlRef} className={`${className ?? ''} ac-highlight`} aria-hidden="true"
          dangerouslySetInnerHTML={{ __html: hlHtml }} />
      )}
      {as === 'textarea' ? <textarea {...fieldProps} /> : <input {...fieldProps} />}
      {open && pos && (
        <ul className="ac-pop" style={{ top: pos.top, left: pos.left }}>
          {items.map((s, i) => (
            <AcItem key={s.label + i} s={s} active={i === active}
              onPick={() => accept(s)} onHover={() => setActive(i)} />
          ))}
        </ul>
      )}
    </span>
  )
}

function AcItem({ s, active, onPick, onHover }: { s: Suggestion; active: boolean; onPick: () => void; onHover: () => void }) {
  const ref = useRef<HTMLLIElement>(null)
  useEffect(() => { if (active) ref.current?.scrollIntoView({ block: 'nearest' }) }, [active])
  return (
    <li ref={ref} className={`ac-item${active ? ' active' : ''}`}
      // mousedown (not click) so we fire before the field's blur closes the popup.
      onMouseDown={e => { e.preventDefault(); onPick() }}
      onMouseEnter={onHover}>
      <span className="ac-label">{s.label}</span>
      {s.kind && <span className="ac-kind">{s.kind}</span>}
      {s.detail && <span className="ac-detail">{s.detail}</span>}
    </li>
  )
}

// ── caret coordinates ──
// Mirror the field into an off-screen div with identical text styling, measure
// the offset of a marker placed at `position`, and return coordinates (relative
// to the field's padding box, scroll already subtracted) for the popup. Adapted
// from the well-known textarea-caret-position technique.
const MIRROR_PROPS = [
  'boxSizing', 'width', 'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft',
  'borderTopWidth', 'borderRightWidth', 'borderBottomWidth', 'borderLeftWidth',
  'fontStyle', 'fontVariant', 'fontWeight', 'fontStretch', 'fontSize', 'fontSizeAdjust',
  'lineHeight', 'fontFamily', 'textAlign', 'textTransform', 'textIndent', 'textDecoration',
  'letterSpacing', 'wordSpacing', 'tabSize',
] as const

function caretXY(el: HTMLInputElement | HTMLTextAreaElement, position: number, isInput: boolean): { top: number; left: number } {
  const div = document.createElement('div')
  const style = div.style
  const computed = window.getComputedStyle(el)
  style.position = 'absolute'
  style.visibility = 'hidden'
  style.whiteSpace = isInput ? 'nowrap' : 'pre-wrap'
  style.wordWrap = isInput ? 'normal' : 'break-word'
  style.overflow = 'hidden'
  for (const p of MIRROR_PROPS) style[p as any] = computed[p as any]
  document.body.appendChild(div)
  div.textContent = el.value.slice(0, position)
  const span = document.createElement('span')
  span.textContent = el.value.slice(position) || '.'
  div.appendChild(span)
  const lineHeight = parseInt(computed.lineHeight) || parseInt(computed.fontSize) || 16
  const top = span.offsetTop - el.scrollTop + lineHeight + 6 // small gap below the caret line
  const left = span.offsetLeft - el.scrollLeft
  document.body.removeChild(div)
  // Keep the popup inside the field horizontally.
  const max = el.clientWidth - 8
  return { top, left: Math.max(0, Math.min(left, max - 160)) }
}
