import { useCallback, useEffect, useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { GridResponse } from '../../types'
import {
  type LucideIcon, Ico, RotateCw, Plus, Minus, ChevronUp, ChevronDown,
  ChevronLeft, ChevronRight, Copy, Maximize2, Filter, FilterX, Info, X,
} from '../../icons'

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
  // Right-click cell menu + the full-value viewer it can open.
  const [menu, setMenu] = useState<{ x: number; y: number; r: number; c: number } | null>(null)
  const [viewer, setViewer] = useState<{ col: string; value: string } | null>(null)

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

  // ── cell-menu actions ──
  const qq = (s: string) => '"' + s.replace(/"/g, '""') + '"'
  const lit = (s: string) => "'" + s.replace(/'/g, "''") + "'"
  const setFilter = (w: string) => { setWhere(w); setPage(0); load(0, w, order) }
  const openDoc = () => app.openTab({
    key: `doc:${connId}:${schema}.${table}`, title: `doc [${table}]`, icon: 'grid',
    view: { type: 'doc', connId, schema, table },
  })

  function cellItems(r: number, c: number): MenuItem[] {
    const col = (result?.columns || [])[c] ?? ''
    const val = rows[r]?.[c] ?? ''
    const cond = val === '' ? `${qq(col)} IS NULL` : `${qq(col)} = ${lit(val)}`
    return [
      { head: col },
      { label: 'Copy value', Icon: Copy, run: () => app.copy(val) },
      { label: 'Copy row', Icon: Copy, run: () => app.copy(rows[r].join('\t')) },
      { label: 'Copy column name', Icon: Copy, run: () => app.copy(col) },
      { sep: true },
      { label: 'Open in value viewer', Icon: Maximize2, run: () => setViewer({ col, value: val }) },
      { sep: true },
      { label: 'Filter by this value', Icon: Filter, run: () => setFilter(cond) },
      { label: 'Add to filter (AND)', Icon: Filter, run: () => setFilter(where.trim() ? `${where.trim()} AND ${cond}` : cond) },
      ...(where ? [{ label: 'Clear filter', Icon: FilterX, run: () => setFilter('') } as MenuItem] : []),
      { sep: true },
      { label: 'Sort ascending', Icon: ChevronUp, run: () => sort(col, 'asc') },
      { label: 'Sort descending', Icon: ChevronDown, run: () => sort(col, 'desc') },
      { sep: true },
      { label: 'Quick documentation', Icon: Info, run: openDoc },
    ]
  }

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
                      <tr key={i}><td className="rownum">{i + 1}</td>{row.map((v, j) => (
                        <td key={j} className="code"
                          onContextMenu={e => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY, r: i, c: j }) }}>{v}</td>
                      ))}</tr>
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

      {menu && <CellMenu x={menu.x} y={menu.y} items={cellItems(menu.r, menu.c)} onClose={() => setMenu(null)} />}
      {viewer && <ValueViewer col={viewer.col} value={viewer.value} onClose={() => setViewer(null)} />}
    </div>
  )
}

interface MenuItem { label?: string; Icon?: LucideIcon; sep?: boolean; head?: string; danger?: boolean; run?: () => void }
const MW = 230, MH = 360

// Lightweight right-click menu for grid cells — reuses the tree menu's styling
// but is self-contained (no NodePayload), driven by a flat item list.
function CellMenu({ x, y, items, onClose }: { x: number; y: number; items: MenuItem[]; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  const left = Math.max(8, Math.min(x, window.innerWidth - MW - 8))
  const top = Math.max(8, Math.min(y, window.innerHeight - MH - 8))
  return (
    <>
      <div className="ctx-backdrop" style={{ position: 'fixed', inset: 0, zIndex: 900 }}
        onClick={onClose} onContextMenu={e => { e.preventDefault(); onClose() }} />
      <div className="ctx-menu" style={{ left, top }}>
        {items.map((it, i) => it.sep ? <div key={i} className="menu-sep" />
          : it.head ? <div key={i} className="ctx-head">{it.head}</div>
          : (
            <button key={i} type="button" className={`menu-item${it.danger ? ' danger' : ''}`}
              onClick={() => { it.run?.(); onClose() }}>
              {it.Icon && <span className="mi-ico"><it.Icon size={15} /></span>}
              <span className="mi-label">{it.label}</span>
            </button>
          ))}
      </div>
    </>
  )
}

// Full-value viewer: the grid truncates wide cells, so this shows the complete
// value (long text / JSON) with a copy button.
function ValueViewer({ col, value, onClose }: { col: string; value: string; onClose: () => void }) {
  const app = useApp()
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal hud-panel hud-panel-glow">
        <div className="modal-head">
          <span className="hud-heading">{col}</span>
          <button type="button" className="ico-btn" onClick={onClose}><X size={16} /></button>
        </div>
        <div className="modal-body">
          <pre className="code value-viewer">{value === '' ? '(empty / null)' : value}</pre>
          <div className="modal-foot">
            <span className="hud-label dim">{value.length} chars</span>
            <span className="tb-grow" />
            <button type="button" className="hud-btn-accent" onClick={() => app.copy(value)}>Copy</button>
            <button type="button" className="hud-btn-cta" onClick={onClose}>Close</button>
          </div>
        </div>
      </div>
    </div>
  )
}
