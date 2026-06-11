import { useEffect, useState } from 'react'
import { api } from '../api'
import { useApp } from '../appctx'
import { X } from '../icons'
import type { Grant, GrantLevel } from '../types'

// Per-connection access grants, shown in the edit modal when scoped-access mode
// is on and the viewer is an admin. A grant maps a Keycloak group path or realm
// role to read/write on this connection; it scopes which connections a non-admin
// reaches without ever exceeding their global capability.
export default function GrantsPanel({ connId }: { connId: number }) {
  const app = useApp()
  const [grants, setGrants] = useState<Grant[]>([])
  const [subject, setSubject] = useState('')
  const [level, setLevel] = useState<GrantLevel>('read')
  const [err, setErr] = useState('')

  const load = () => api.listGrants(connId).then(r => setGrants(r.grants)).catch(e => setErr(String(e.message || e)))
  useEffect(() => { load() }, [connId])

  const add = () => {
    const s = subject.trim()
    if (!s) return
    setErr('')
    api.setGrant(connId, s, level)
      .then(() => { setSubject(''); app.notify('Grant saved'); load() })
      .catch(e => setErr(String(e.message || e)))
  }

  const remove = (g: Grant) => {
    setErr('')
    api.deleteGrant(connId, g.id)
      .then(() => { app.notify('Grant removed'); load() })
      .catch(e => setErr(String(e.message || e)))
  }

  return (
    <div className="grants-panel">
      <label className="hud-label">Access grants <span className="dim">(group or realm role → level)</span></label>
      {err && <div className="alert error code">{err}</div>}
      {grants.length > 0 && (
        <ul className="grant-list">
          {grants.map(g => (
            <li key={g.id} className="grant-row">
              <span className="grant-subject code">{g.subject}</span>
              <span className={`grant-level ${g.level}`}>{g.level}</span>
              <button type="button" className="ico-btn" title="Remove grant" onClick={() => remove(g)}><X size={13} /></button>
            </li>
          ))}
        </ul>
      )}
      <div className="grant-add">
        <input className="hud-input code" placeholder="/team-a or dbm-write" value={subject}
          onChange={e => setSubject(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); add() } }} />
        <select className="hud-input" value={level} onChange={e => setLevel(e.target.value as GrantLevel)}>
          <option value="read">read</option>
          <option value="write">write</option>
        </select>
        <button type="button" className="hud-btn-accent" onClick={add}>Add</button>
      </div>
    </div>
  )
}
