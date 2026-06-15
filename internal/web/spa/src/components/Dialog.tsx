import { useEffect, useRef, useState } from 'react'
import type { DialogRequest } from '../appctx'
import { X } from '../icons'

// HUD-styled modal that stands in for window.confirm / window.prompt. Driven by
// the app context (see App.tsx): a single instance renders the current request
// and calls onResolve with the chosen button value / entered text, or null when
// dismissed (Esc, overlay click, Cancel, or the × button).
export default function Dialog({ req, onResolve }: {
  req: DialogRequest; onResolve: (value: string | null) => void
}) {
  const [text, setText] = useState(req.kind === 'prompt' ? (req.initial ?? '') : '')
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (req.kind === 'prompt') inputRef.current?.focus()
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onResolve(null) }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [req, onResolve])

  const variantClass = (v?: string) =>
    v === 'danger' ? 'btn-danger' : v === 'accent' ? 'hud-btn-accent' : 'hud-btn-cta'

  return (
    <div className="modal-overlay">
      <div className="modal hud-panel hud-panel-glow dialog">
        <div className="modal-head">
          <span className="hud-heading">{req.title}</span>
          <button type="button" className="ico-btn" onClick={() => onResolve(null)}><X size={16} /></button>
        </div>

        {req.kind === 'prompt' ? (
          <form className="modal-form" onSubmit={e => { e.preventDefault(); onResolve(text) }}>
            <div className="modal-body">
            {req.body && <p className="dialog-body dim">{req.body}</p>}
            <input ref={inputRef} className="hud-input" value={text} placeholder={req.placeholder}
              onChange={e => setText(e.target.value)} />
            </div>
            <div className="modal-foot">
              <span className="tb-grow" />
              <button type="button" className="hud-btn-accent" onClick={() => onResolve(null)}>Cancel</button>
              <button type="submit" className="hud-btn-cta">{req.submitLabel ?? 'OK'}</button>
            </div>
          </form>
        ) : (
          <>
            <div className="modal-body">
              {req.body && <p className="dialog-body dim">{req.body}</p>}
            </div>
            <div className="modal-foot">
              <span className="tb-grow" />
              <button type="button" className="hud-btn-accent" onClick={() => onResolve(null)}>{req.cancelLabel ?? 'Cancel'}</button>
              {req.buttons.map(b => (
                <button key={b.value} type="button" className={variantClass(b.variant)}
                  onClick={() => onResolve(b.value)}>{b.label}</button>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
