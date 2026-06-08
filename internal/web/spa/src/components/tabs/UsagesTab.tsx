import { useEffect, useState } from 'react'
import { api } from '../../api'
import type { UsagesResponse } from '../../types'

// Find-usages tab: inbound foreign keys referencing this table ("usagesResult").
export default function UsagesTab({ connId, schema, table }: { connId: number; schema: string; table: string }) {
  const [d, setD] = useState<UsagesResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.usages(connId, schema, table).then(setD).catch(e => setErr(String(e.message || e)))
  }, [connId, schema, table])

  if (err) return <div className="doc-pane p-4"><div className="alert error code">{err}</div></div>
  if (!d) return <div className="doc-pane p-4"><p className="dim">loading…</p></div>

  const usages = d.usages || []
  return (
    <div className="doc-pane p-4">
      <div className="hud-heading">Usages of {d.schema}.{d.table}</div>
      {d.error && <div className="alert error code">{d.error}</div>}
      {usages.length === 0 ? <p className="dim">No foreign keys reference this table.</p>
        : (
          <div className="tablewrap">
            <table className="data">
              <thead><tr><th>referencing table</th><th>constraint</th><th>definition</th></tr></thead>
              <tbody>{usages.map((u, i) => <tr key={i}><td>{u.Schema}.{u.Table}</td><td>{u.Name}</td><td className="code">{u.Def}</td></tr>)}</tbody>
            </table>
          </div>
        )}
    </div>
  )
}
