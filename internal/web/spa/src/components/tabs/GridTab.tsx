import { useCallback, useEffect, useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { GridResponse } from '../../types'
import { Ico, RotateCw, Plus, Minus, ChevronUp, ChevronDown, ChevronLeft, ChevronRight } from '../../icons'

// Data grid tab: paginated read-only browse with WHERE / ORDER BY filters and
// per-column sort arrows. Equivalent to the "grid" + "gridResult" partials.
export default function GridTab({ connId, schema, table }: { connId: number; schema: string; table: string }) {
  const app = useApp()
  const conn = app.connById(connId)
  const [where, setWhere] = useState('')
  const [order, setOrder] = useState('')
  const [page, setPage] = useState(0)
  const [data, setData] = useState<GridResponse | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback((p: number, w: string, o: string) => {
    setLoading(true)
    api.grid(connId, { schema, table, where: w, order: o, page: p })
      .then(setData).catch(e => setData({ result: null, readOnly: true, page: p, error: String(e.message || e) }))
      .finally(() => setLoading(false))
  }, [connId, schema, table])

  useEffect(() => { load(page, where, order) }, [load, page]) // eslint-disable-line react-hooks/exhaustive-deps

  const apply = (e: React.FormEvent) => { e.preventDefault(); setPage(0); load(0, where, order) }
  const sort = (col: string, dir: 'asc' | 'desc') => { const o = `"${col}" ${dir}`; setOrder(o); setPage(0); load(0, where, o) }

  const result = data?.result
  const rows = result?.rows || []
  const readOnly = data?.readOnly ?? (conn ? conn.readOnly || !app.caps.write : true)

  return (
    <div className="grid-pane">
      <div className="grid-toolbar">
        <button className="tb-ico" title="refresh" onClick={() => load(page, where, order)}><RotateCw size={16} /></button>
        <span className="tb-sep" />
        <button className="tb-ico" title="add row (coming soon)" disabled><Plus size={16} /></button>
        <button className="tb-ico" title="remove row (coming soon)" disabled><Minus size={16} /></button>
        <span className="tb-sep" />
        <span className="tb-chip hud-label">Tx: Auto</span>
        <span className="tb-grow" />
        {readOnly && <span className="ro">READ-ONLY</span>}
        {conn && <span className="tb-chip hud-label">{conn.kind}@{conn.host}</span>}
      </div>

      <form className="filter-bar" onSubmit={apply}>
        <span className="fb-key hud-label">WHERE</span>
        <input className="fb-input code" value={where} onChange={e => setWhere(e.target.value)} placeholder="user_id = 4 and group_code = 0" />
        <span className="fb-key hud-label">ORDER BY</span>
        <input className="fb-input code" value={order} onChange={e => setOrder(e.target.value)} placeholder="event_time desc" />
        <button className="hud-btn-accent sm" type="submit">apply</button>
      </form>

      <div className="grid-body">
        {data?.error ? <div className="alert error code">{data.error}</div>
          : !result ? <p className="dim">{loading ? 'loading…' : ''}</p>
          : !result.isSelect ? <p className="ok hud-label">{result.command} · {result.rowsAffected} rows affected · {result.duration}</p>
          : rows.length === 0 ? <p className="dim">0 rows · {result.duration}</p>
          : (
            <>
              <div className="tablewrap grid-table">
                <table className="data">
                  <thead><tr>
                    <th className="rownum">#</th>
                    {(result.columns || []).map((c, i) => (
                      <th key={i}>
                        <span className="th-name">{c}</span>
                        <span className="th-tools">
                          <a className="th-sort" title="sort asc" onClick={() => sort(c, 'asc')}><ChevronUp size={13} /></a>
                          <a className="th-sort" title="sort desc" onClick={() => sort(c, 'desc')}><ChevronDown size={13} /></a>
                        </span>
                      </th>
                    ))}
                  </tr></thead>
                  <tbody>
                    {rows.map((row, i) => (
                      <tr key={i}><td className="rownum">{i + 1}</td>{row.map((v, j) => <td key={j} className="code">{v}</td>)}</tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className="grid-meta hud-label dim">{rows.length} rows{result.truncated ? ' · truncated at 1000' : ''} · {result.duration}</p>
            </>
          )}
      </div>

      <div className="grid-footer hud-label">
        {conn && <Ico name={conn.kind} />}
        <span>{schema}.{table}</span>
        <span className="tb-grow" />
        {page > 0 && <a className="pg-btn" onClick={() => setPage(p => p - 1)}><ChevronLeft size={14} /> prev</a>}
        <span>page {page + 1}</span>
        {rows.length > 0 && <a className="pg-btn" onClick={() => setPage(p => p + 1)}>next <ChevronRight size={14} /></a>}
      </div>
    </div>
  )
}
