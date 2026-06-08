import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { GridResponse, QueryResponse } from '../../types'
import {
  type LucideIcon, Ico, RotateCw, Plus, Minus, ChevronUp, ChevronDown,
  ChevronLeft, ChevronRight, Copy, Maximize2, Filter, FilterX, Info, X,
  SquarePen, TableProperties, Sigma, Undo2, FileCode, Code, Trash2, ArrowRight,
  Search, Download,
} from '../../icons'

interface MenuItem {
  label?: string; Icon?: LucideIcon; sep?: boolean; head?: string
  danger?: boolean; disabled?: boolean; key?: string; run?: () => void; children?: MenuItem[]
}

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
  // Right-click menu (cell or table-level) and the modals it can open.
  const [menu, setMenu] = useState<{ x: number; y: number; items: MenuItem[] } | null>(null)
  const [viewer, setViewer] = useState<{ col: string; value: string } | null>(null)
  const [record, setRecord] = useState<number | null>(null)
  const [agg, setAgg] = useState<string | null>(null)

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
  const cols = result?.columns || []
  const rows = result?.rows || []
  const readOnly = data?.readOnly ?? (conn ? conn.readOnly || !app.caps.write : true)

  // helpers shared by the menus
  const qq = (s: string) => '"' + s.replace(/"/g, '""') + '"'
  const lit = (s: string) => "'" + s.replace(/'/g, "''") + "'"
  const qual = `${qq(schema)}.${qq(table)}`
  const whereSuffix = where.trim() ? ` WHERE ${where.trim()}` : ''
  const setFilter = (w: string) => { setWhere(w); setPage(0); load(0, w, order) }
  const openDoc = () => app.openTab({
    key: `doc:${connId}:${schema}.${table}`, title: `doc [${table}]`, icon: 'grid',
    view: { type: 'doc', connId, schema, table },
  })

  // Row → various clipboard formats (the "data extractors").
  const sqlInsert = (r: number) =>
    `INSERT INTO ${qual} (${cols.map(qq).join(', ')}) VALUES (${rows[r].map(v => v === '' ? 'NULL' : lit(v)).join(', ')});`
  const jsonRow = (r: number) => JSON.stringify(Object.fromEntries(cols.map((c, i) => [c, rows[r][i]])))
  const csvCell = (s: string) => /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
  const csvRow = (r: number) => rows[r].map(csvCell).join(',')
  const tableTsv = () => [cols.join('\t'), ...rows.map(r => r.join('\t'))].join('\n')

  const copySum = (col: string) =>
    api.query(connId, `SELECT sum(${qq(col)}) FROM ${qual}${whereSuffix}`, true)
      .then(r => {
        if (r.error) return app.notify(r.error, 'error')
        const v = r.result?.rows?.[0]?.[0]
        app.copy(v ?? ''); app.notify(`SUM(${col}) = ${v ?? '∅'}`)
      })
      .catch(e => app.notify(String(e.message || e), 'error'))

  const fullTextSearch = () => {
    const term = window.prompt('Full-text search — matches any column (cast to text):')
    if (!term) return
    setFilter('(' + cols.map(c => `${qq(c)}::text ILIKE ${lit('%' + term + '%')}`).join(' OR ') + ')')
  }

  // Unified right-click menu — mirrors the DataGrip data-editor menu. One menu
  // serves both cell and table-area clicks: the cell-specific block only appears
  // when there's a row under the cursor (rows[r] exists); otherwise it collapses
  // to the table-level actions with a schema.table header. Write-only actions
  // are present but disabled (this build browses read-only), exactly like
  // DataGrip greys what the current context can't do.
  function menuItems(r: number, c: number): MenuItem[] {
    const cell = rows[r] !== undefined
    const col = cols[c] ?? ''
    const val = rows[r]?.[c] ?? ''
    const eq = val === '' ? `${qq(col)} IS NULL` : `${qq(col)} = ${lit(val)}`
    const neq = val === '' ? `${qq(col)} IS NOT NULL` : `${qq(col)} <> ${lit(val)}`
    return [
      { head: cell ? col : `${schema}.${table}` },
      ...(cell ? [
        { label: 'Edit', Icon: SquarePen, key: 'Enter', disabled: true },
        { label: 'Show record view', Icon: TableProperties, run: () => setRecord(r) },
        { label: 'Open in value editor', Icon: Maximize2, run: () => setViewer({ col, value: val }) },
        { label: 'Show aggregate view', Icon: Sigma, run: () => setAgg(col) },
        { sep: true },
        { label: 'Revert selected', Icon: Undo2, disabled: true },
        { label: 'Copy using data extractor (SQL Inserts)', Icon: FileCode, key: 'Ctrl+C', run: () => app.copy(sqlInsert(r)) },
        { label: 'Change data extractor', Icon: Code, children: [
          { label: 'SQL Inserts', run: () => app.copy(sqlInsert(r)) },
          { label: 'JSON', run: () => app.copy(jsonRow(r)) },
          { label: 'CSV', run: () => app.copy(csvRow(r)) },
          { label: 'TSV', run: () => app.copy(rows[r].join('\t')) },
        ] },
        { label: 'Copy aggregation result (SUM)', Icon: Sigma, key: 'Ctrl+Shift+C', run: () => copySum(col) },
        { sep: true },
        { label: 'Add row', Icon: Plus, key: 'Alt+Ins', disabled: true },
        { label: 'Delete rows', Icon: Trash2, key: 'Ctrl+Y', disabled: true },
        { sep: true },
        { label: 'Go to', Icon: ArrowRight, children: [
          { label: 'First page', disabled: page === 0, run: () => setPage(0) },
          { label: 'Previous page', disabled: page === 0, run: () => setPage(p => Math.max(0, p - 1)) },
          { label: 'Next page', disabled: rows.length === 0, run: () => setPage(p => p + 1) },
          { label: 'Refresh', run: () => load(page, where, order) },
        ] },
        { label: 'Filter by', Icon: Filter, children: [
          { label: `${col} equals this value`, run: () => setFilter(eq) },
          { label: `${col} ≠ this value`, run: () => setFilter(neq) },
          { label: `${col} IS NULL`, run: () => setFilter(`${qq(col)} IS NULL`) },
          { label: `${col} IS NOT NULL`, run: () => setFilter(`${qq(col)} IS NOT NULL`) },
          { label: 'Add to filter (AND)', run: () => setFilter(where.trim() ? `${where.trim()} AND ${eq}` : eq) },
          ...(where ? [{ label: 'Clear filter', run: () => setFilter('') }] : []),
        ] },
        { sep: true },
      ] as MenuItem[] : []),
      // table-level actions (always shown)
      { label: 'Refresh', Icon: RotateCw, run: () => load(page, where, order) },
      { sep: true },
      { label: 'Copy column names', Icon: Copy, run: () => app.copy(cols.join('\t')) },
      { label: 'Export table to clipboard', Icon: Download, run: () => app.copy(tableTsv()) },
      { label: 'Full-text search…', Icon: Search, key: 'Ctrl+Alt+Shift+F', run: fullTextSearch },
      ...(where ? [{ label: 'Clear filter', Icon: FilterX, run: () => setFilter('') } as MenuItem] : []),
      { sep: true },
      { label: 'Quick documentation', Icon: Info, key: 'Ctrl+Q', run: openDoc },
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
        {conn && <span className="tb-chip conn-chip hud-label" title={`${conn.kind}@${conn.host}`}>{conn.kind}@{conn.host}</span>}
      </div>

      <form className="filter-bar" onSubmit={apply}>
        <span className="fb-key hud-label">WHERE</span>
        <input className="fb-input code" value={where} onChange={e => setWhere(e.target.value)} placeholder="user_id = 4 and group_code = 0" />
        <span className="fb-key hud-label">ORDER BY</span>
        <input className="fb-input code" value={order} onChange={e => setOrder(e.target.value)} placeholder="event_time desc" />
        <button className="hud-btn-accent sm" type="submit">apply</button>
      </form>

      <div className="grid-body"
        onContextMenu={e => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY, items: menuItems(0, 0) }) }}>
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
                    {cols.map((c, i) => (
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
                          onContextMenu={e => { e.preventDefault(); e.stopPropagation(); setMenu({ x: e.clientX, y: e.clientY, items: menuItems(i, j) }) }}>{v}</td>
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

      {menu && <CellMenu x={menu.x} y={menu.y} items={menu.items} onClose={() => setMenu(null)} />}
      {viewer && <ValueViewer col={viewer.col} value={viewer.value} onClose={() => setViewer(null)} />}
      {record != null && <RecordView cols={cols} row={rows[record] || []} title={`${schema}.${table}`} onClose={() => setRecord(null)} />}
      {agg && <AggregateView connId={connId} col={agg} sql={`SELECT count(*) AS "rows", count(${qq(agg)}) AS "non null", count(DISTINCT ${qq(agg)}) AS "distinct", min(${qq(agg)}) AS "min", max(${qq(agg)}) AS "max" FROM ${qual}${whereSuffix}`} onClose={() => setAgg(null)} />}
    </div>
  )
}

const MW = 260, MH = 380

// Right-click menu for grid cells / table area — reuses the tree menu's styling
// and supports one level of fly-out submenus, keyboard-shortcut hints and
// disabled items (mirrors the DataGrip data-editor menu).
function CellMenu({ x, y, items, onClose }: { x: number; y: number; items: MenuItem[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const [openSub, setOpenSub] = useState<number | null>(null)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    const onDown = (e: Event) => { if (!ref.current?.contains(e.target as Node)) onClose() }
    window.addEventListener('keydown', onKey)
    window.addEventListener('mousedown', onDown, true)
    window.addEventListener('contextmenu', onDown, true)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('mousedown', onDown, true)
      window.removeEventListener('contextmenu', onDown, true)
    }
  }, [onClose])
  const left = Math.max(8, Math.min(x, window.innerWidth - MW - 8))
  const top = Math.max(8, Math.min(y, window.innerHeight - MH - 8))
  const flip = left + MW + 220 > window.innerWidth
  const fire = (it: MenuItem) => { if (it.disabled || it.children) return; it.run?.(); onClose() }
  return (
    <>
      <div className="ctx-backdrop" onClick={onClose} onContextMenu={e => { e.preventDefault(); onClose() }} />
      <div ref={ref} className="ctx-menu" style={{ left, top }}>
        {items.map((it, i) => it.sep ? <div key={i} className="menu-sep" />
          : it.head ? <div key={i} className="ctx-head">{it.head}</div>
          : (
            <div key={i} className="menu-row"
              onMouseEnter={() => it.children && setOpenSub(i)}
              onMouseLeave={() => it.children && setOpenSub(s => (s === i ? null : s))}>
              <button type="button" disabled={it.disabled}
                className={`menu-item${it.children ? ' has-children' : ''}${it.danger ? ' danger' : ''}${openSub === i ? ' expanded' : ''}`}
                onClick={() => it.children ? setOpenSub(s => s === i ? null : i) : fire(it)}>
                {it.Icon && <span className="mi-ico"><it.Icon size={15} /></span>}
                <span className="mi-label">{it.label}</span>
                {it.key && <span className="ctx-key">{it.key}</span>}
                {it.children && <span className="mi-caret"><ChevronRight size={13} /></span>}
              </button>
              {it.children && openSub === i && (
                <div className={`ctx-submenu${flip ? ' flip' : ''}`}>
                  {it.children.map((s, j) => (
                    <button key={j} type="button" disabled={s.disabled}
                      className={`menu-item${s.danger ? ' danger' : ''}`}
                      onClick={() => { s.run?.(); onClose() }}>
                      <span className="mi-label">{s.label}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>
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

// Record view: the whole row laid out vertically as column → value, handy when a
// table is wider than the screen.
function RecordView({ cols, row, title, onClose }: { cols: string[]; row: string[]; title: string; onClose: () => void }) {
  const app = useApp()
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  const asJson = () => app.copy(JSON.stringify(Object.fromEntries(cols.map((c, i) => [c, row[i]])), null, 2))
  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal hud-panel hud-panel-glow">
        <div className="modal-head">
          <span className="hud-heading">Record · {title}</span>
          <button type="button" className="ico-btn" onClick={onClose}><X size={16} /></button>
        </div>
        <div className="modal-body">
          <div className="tablewrap" style={{ maxHeight: '60vh' }}>
            <table className="data record-view">
              <tbody>
                {cols.map((c, i) => (
                  <tr key={i}>
                    <td className="rv-key hud-label">{c}</td>
                    <td className="code rv-val">{row[i] === '' ? <span className="dim">null</span> : row[i]}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="modal-foot">
            <span className="tb-grow" />
            <button type="button" className="hud-btn-accent" onClick={asJson}>Copy JSON</button>
            <button type="button" className="hud-btn-cta" onClick={onClose}>Close</button>
          </div>
        </div>
      </div>
    </div>
  )
}

// Aggregate view: count / distinct / min / max for a column (honouring the
// current WHERE), run on open.
function AggregateView({ connId, col, sql, onClose }: { connId: number; col: string; sql: string; onClose: () => void }) {
  const [resp, setResp] = useState<QueryResponse | null>(null)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    api.query(connId, sql, true).then(setResp).catch(e => setResp({ readOnly: true, error: String(e.message || e) }))
    return () => window.removeEventListener('keydown', onKey)
  }, [connId, sql, onClose])
  const r = resp?.result
  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal hud-panel hud-panel-glow">
        <div className="modal-head">
          <span className="hud-heading">Aggregate · {col}</span>
          <button type="button" className="ico-btn" onClick={onClose}><X size={16} /></button>
        </div>
        <div className="modal-body">
          {!resp ? <p className="dim">computing…</p>
            : resp.error ? <div className="alert error code">{resp.error}</div>
            : r && r.rows?.[0] ? (
              <table className="data record-view">
                <tbody>
                  {(r.columns || []).map((c, i) => (
                    <tr key={i}><td className="rv-key hud-label">{c}</td><td className="code rv-val">{r.rows![0][i]}</td></tr>
                  ))}
                </tbody>
              </table>
            ) : <p className="dim">no result</p>}
          <div className="modal-foot">
            <span className="tb-grow" />
            <button type="button" className="hud-btn-cta" onClick={onClose}>Close</button>
          </div>
        </div>
      </div>
    </div>
  )
}
