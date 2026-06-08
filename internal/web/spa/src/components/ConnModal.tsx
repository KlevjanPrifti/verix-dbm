import { useEffect, useState } from 'react'
import { api, type ConnInput } from '../api'
import { useApp } from '../appctx'
import { X, Check } from '../icons'

type Form = ConnInput & { port: number }

const blank = (kind: string): Form => ({
  name: '', kind, host: '', port: kind === 'redis' ? 6379 : 5432,
  dbname: '', username: '', password: '', passwordEnc: '', options: '', readOnly: false,
})

// Parse postgresql://user:pass@host:5432/db?opts into form fields (ported from
// the old window.parseConnUrl).
function parseConnUrl(raw: string): Partial<Form> | null {
  const url = raw.trim()
  if (!url) return null
  let u: URL
  try { u = new URL(url) } catch { return null }
  if (!u.hostname) return null
  let kind = u.protocol.replace(':', '').toLowerCase()
  if (kind === 'postgresql' || kind === 'postgres' || kind === 'pg') kind = 'postgres'
  else if (kind === 'redis' || kind === 'rediss' || kind === 'valkey') kind = 'redis'
  return {
    kind,
    host: u.hostname,
    port: Number(u.port) || (kind === 'redis' ? 6379 : 5432),
    username: decodeURIComponent(u.username || ''),
    password: decodeURIComponent(u.password || ''),
    dbname: decodeURIComponent(u.pathname.replace(/^\//, '')),
    options: u.search ? u.search.slice(1) : '',
  }
}

// Data Sources & Drivers modal — create a new connection or edit an existing
// one. Replaces the "connModal" / "connEditModal" partials.
export default function ConnModal({ mode, editId, initialKind, onClose, onSaved }: {
  mode: 'create' | 'edit'
  editId?: number
  initialKind?: string
  onClose: () => void
  onSaved: () => void
}) {
  const app = useApp()
  const [f, setF] = useState<Form>(blank(initialKind || 'postgres'))
  const [test, setTest] = useState<{ ok: boolean; msg: string } | null>(null)
  const [err, setErr] = useState('')
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF(p => ({ ...p, [k]: v }))

  useEffect(() => {
    if (mode === 'edit' && editId != null) {
      api.getConnection(editId).then(r => {
        setF({ ...r.connection, password: '', passwordEnc: r.passwordEnc })
      }).catch(e => setErr(String(e.message || e)))
    }
  }, [mode, editId])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const applyUrl = (raw: string) => {
    const p = parseConnUrl(raw)
    if (!p) return
    setF(prev => {
      const next = { ...prev, ...p }
      if (!prev.name && p.host) next.name = (p.username ? p.username + '@' : '') + p.host
      return next
    })
  }

  const onTest = () => {
    setTest(null)
    api.testConnection(f).then(r => setTest({ ok: r.ok, msg: r.ok ? 'connection ok' : String(r.error) }))
      .catch(e => setTest({ ok: false, msg: String(e.message || e) }))
  }

  const save = (asCopy: boolean) => (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    const done = () => { app.notify('Saved'); onSaved() }
    const fail = (x: any) => setErr(String(x.message || x))
    if (mode === 'edit' && editId != null && !asCopy) {
      api.updateConnection(editId, f).then(done).catch(fail)
    } else {
      api.createConnection(f).then(done).catch(fail)
    }
  }

  const title = mode === 'edit' ? <>Data Sources &amp; Drivers <span className="dim">· edit</span></> : 'Data Sources & Drivers'

  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal hud-panel hud-panel-glow">
        <div className="modal-head">
          <span className="hud-heading">{title}</span>
          <button type="button" className="ico-btn" onClick={onClose}><X size={16} /></button>
        </div>
        <form className="modal-body" onSubmit={save(false)}>
          {err && <div className="alert error code">{err}</div>}
          <div className="mrow"><label className="hud-label">URL <span className="dim">(paste to autofill the fields below)</span></label>
            <input className="hud-input code" placeholder="postgresql://user:pass@host:5432/dbname" onChange={e => applyUrl(e.target.value)} /></div>
          <div className="mrow"><label className="hud-label">Name</label>
            <input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} placeholder="postgres@localhost" /></div>
          <div className="mrow"><label className="hud-label">Driver</label>
            <select className="hud-input" value={f.kind}
              onChange={e => { const kind = e.target.value; setF(p => ({ ...p, kind, port: kind === 'redis' ? 6379 : 5432 })) }}>
              <option value="postgres">PostgreSQL</option>
              <option value="redis">Redis / Valkey</option>
            </select>
          </div>
          <div className="mrow2">
            <div className="mcol"><label className="hud-label">Host</label>
              <input className="hud-input" required value={f.host} onChange={e => set('host', e.target.value)} placeholder="localhost" /></div>
            <div className="mcol port"><label className="hud-label">Port</label>
              <input className="hud-input" value={f.port} onChange={e => set('port', Number(e.target.value) || 0)} /></div>
          </div>
          <div className="mrow2">
            <div className="mcol"><label className="hud-label">User</label>
              <input className="hud-input" value={f.username} onChange={e => set('username', e.target.value)} placeholder="postgres" /></div>
            <div className="mcol"><label className="hud-label">Password</label>
              <input className="hud-input" type="password" value={f.password} onChange={e => set('password', e.target.value)}
                placeholder={mode === 'edit' ? '•••• unchanged' : ''} /></div>
          </div>
          <div className="mrow"><label className="hud-label">Database <span className="dim">(pg name / redis db #)</span></label>
            <input className="hud-input" value={f.dbname} onChange={e => set('dbname', e.target.value)} placeholder="postgres" /></div>
          <div className="mrow"><label className="hud-label">Options</label>
            <input className="hud-input" value={f.options} onChange={e => set('options', e.target.value)} placeholder="sslmode=disable" /></div>
          <label className="check"><input type="checkbox" checked={f.readOnly} onChange={e => set('readOnly', e.target.checked)} /> <span className="hud-label">Read-only</span></label>
          <div className="modal-foot">
            <button type="button" className="hud-btn-accent" onClick={onTest}>Test Connection</button>
            <span className={`test-result ${test ? (test.ok ? 'ok' : 'bad') : ''} hud-label code`}>
              {test && (test.ok ? <Check size={13} /> : <X size={13} />)}{test ? ' ' + test.msg : ''}
            </span>
            <span className="tb-grow" />
            {mode === 'edit' && (
              <button type="button" className="hud-btn-accent" onClick={e => save(true)(e)}
                title="Create a new connection from these values">Save as copy</button>
            )}
            <button type="button" className="hud-btn-accent" onClick={onClose}>Cancel</button>
            <button type="submit" className="hud-btn-cta">{mode === 'edit' ? 'Save' : 'OK'}</button>
          </div>
        </form>
      </div>
    </div>
  )
}
