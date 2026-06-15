import { useEffect, useState } from 'react'
import { api } from '../api'
import type { AuditRow } from '../types'
import { X, Check } from '../icons'

// Audit log (admin-only): the last 200 entries. The old build was a full page;
// here it's an overlay so the workbench stays put.
export default function AuditModal({ onClose }: { onClose: () => void }) {
  const [rows, setRows] = useState<AuditRow[] | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.audit().then(r => setRows(r.rows)).catch(e => setErr(String(e.message || e)))
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return (
    <div className="modal-overlay">
      <div className="modal hud-panel hud-panel-glow" style={{ maxWidth: '60rem', width: '90vw' }}>
        <div className="modal-head">
          <span className="hud-heading">Audit log <span className="dim">· last 200</span></span>
          <span className="tb-grow" />
          <button type="button" className="hud-btn-accent" onClick={() => api.auditExport('jsonl').catch(e => setErr(String(e.message || e)))}>Export JSONL</button>
          <button type="button" className="hud-btn-accent" onClick={() => api.auditExport('csv').catch(e => setErr(String(e.message || e)))}>Export CSV</button>
          <button type="button" className="ico-btn" onClick={onClose}><X size={16} /></button>
        </div>
        <div className="modal-body">
          {err && <div className="alert error code">{err}</div>}
          {!rows && !err && <p className="dim">loading…</p>}
          {rows && (
            <div className="tablewrap" style={{ maxHeight: '70vh' }}>
              <table className="data">
                <thead><tr><th>When</th><th>User</th><th>Conn</th><th>Action</th><th>Detail</th><th>OK</th></tr></thead>
                <tbody>
                  {rows.map((a, i) => (
                    <tr key={i}>
                      <td className="code">{a.ts}</td>
                      <td>{a.user}</td>
                      <td>{a.connId || ''}</td>
                      <td className="code">{a.action}</td>
                      <td className="code">{a.detail}</td>
                      <td>{a.success ? <Check size={15} className="ok" /> : <X size={15} className="bad" />}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
