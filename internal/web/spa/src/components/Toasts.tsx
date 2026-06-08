export interface Notice { id: number; msg: string; kind: 'ok' | 'error' }

// Transient bottom-right notifications (copy confirmations, errors). Reuses the
// .alert palette from hud.css.
export default function Toasts({ notices }: { notices: Notice[] }) {
  if (!notices.length) return null
  return (
    <div style={{ position: 'fixed', right: '1rem', bottom: '1rem', display: 'flex', flexDirection: 'column', gap: '.5rem', zIndex: 1000 }}>
      {notices.map(n => (
        <div key={n.id} className={`alert ${n.kind === 'error' ? 'error' : 'ok'}`} style={{ minWidth: '12rem' }}>
          {n.msg}
        </div>
      ))}
    </div>
  )
}
