import { useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { QueryResponse } from '../../types'
import ResultTable from '../ResultTable'
import { Play } from '../../icons'

// Postgres query console: run SQL, with a confirmation gate for destructive
// statements (mirrors the "consolePG" + "queryResult" partials).
export default function ConsoleTab({ connId, initialSql }: { connId: number; initialSql?: string }) {
  const app = useApp()
  const conn = app.connById(connId)
  const [sql, setSql] = useState(initialSql ?? '')
  const [resp, setResp] = useState<QueryResponse | null>(null)
  const [running, setRunning] = useState(false)
  const readOnly = conn ? conn.readOnly || !app.caps.write : false

  const run = (confirm = false) => {
    setRunning(true)
    api.query(connId, sql, confirm).then(setResp)
      .catch(e => setResp({ readOnly, error: String(e.message || e) }))
      .finally(() => setRunning(false))
  }

  const onSubmit = (e: React.FormEvent) => { e.preventDefault(); run(false) }
  const onKey = (e: React.KeyboardEvent) => { if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); run(false) } }

  return (
    <div className="console-pane">
      <form className="console-form" onSubmit={onSubmit}>
        <div className="console-toolbar">
          <button className="hud-btn-cta sm" type="submit" disabled={running}><Play size={13} /> Run</button>
          {readOnly && <span className="ro">READ-ONLY</span>}
          {conn && <span className="tb-chip conn-chip hud-label" title={`${conn.kind}@${conn.host}`}>{conn.kind}@{conn.host}</span>}
          {running && <span className="hud-label">running…</span>}
        </div>
        <textarea className="hud-input code console-editor" value={sql} onChange={e => setSql(e.target.value)}
          onKeyDown={onKey} placeholder="select * from … limit 100;" />
      </form>
      {resp && (
        <div className="console-result">
          {resp.error && <div className="alert error code">{resp.error}</div>}
          {resp.needConfirm && (
            <div className="alert warn">
              <p className="hud-label">This statement looks destructive. Confirm to run:</p>
              <pre className="code">{resp.sql}</pre>
              <button className="btn-danger" type="button" onClick={() => run(true)}>Yes, run it</button>
            </div>
          )}
          {resp.result && <ResultTable r={resp.result} />}
        </div>
      )}
    </div>
  )
}
