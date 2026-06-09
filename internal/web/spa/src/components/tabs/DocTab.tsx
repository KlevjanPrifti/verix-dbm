import { useEffect, useState } from 'react'
import { api } from '../../api'
import type { DocResponse } from '../../types'
import { KeyRound } from '../../icons'

// Quick documentation tab: columns + keys + indexes + comment ("docResult").
export default function DocTab({ connId, schema, table }: { connId: number; schema: string; table: string }) {
  const [d, setD] = useState<DocResponse | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    api.doc(connId, schema, table).then(setD).catch(e => setErr(String(e.message || e)))
  }, [connId, schema, table])

  if (err) return <div className="doc-pane"><div className="doc-scroll p-4"><div className="alert error code">{err}</div></div></div>
  if (!d) return <div className="doc-pane"><div className="doc-scroll p-4"><p className="dim">loading…</p></div></div>

  return (
    <div className="doc-pane">
      <div className="doc-scroll p-4">
        <div className="hud-heading">{d.schema}.{d.table}</div>
        {d.comment && <p className="dim">{d.comment}</p>}
        <h4 className="hud-label doc-h">Columns</h4>
        <div className="tablewrap">
          <table className="data">
            <thead><tr><th>name</th><th>type</th><th>nullable</th><th>default</th></tr></thead>
            <tbody>
              {d.columns.map(c => (
                <tr key={c.name}>
                  <td>{c.name}{c.pk && <KeyRound size={13} className="ico-pk" style={{ verticalAlign: '-2px', marginLeft: '.3rem' }} />}</td>
                  <td className="code">{c.typeText}</td>
                  <td>{c.notNull ? 'NOT NULL' : 'null'}</td>
                  <td className="code">{c.default}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {d.keys && d.keys.length > 0 && (
          <><h4 className="hud-label doc-h">Keys</h4>
            <ul className="doc-list">{d.keys.map(k => <li key={k.Name} className="code">{k.Name} — {k.Def}</li>)}</ul></>
        )}
        {d.indexes && d.indexes.length > 0 && (
          <><h4 className="hud-label doc-h">Indexes</h4>
            <ul className="doc-list">{d.indexes.map(i => <li key={i.Name} className="code">{i.Def}</li>)}</ul></>
        )}
      </div>
    </div>
  )
}
