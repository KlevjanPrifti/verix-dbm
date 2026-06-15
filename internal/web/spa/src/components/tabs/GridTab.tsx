import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { Column, GridResponse, QueryResponse } from '../../types'
import CodeField, { qIdent, type Suggestion } from '../Autocomplete'
import { kindEngine } from '../../dbkinds'
import {
  type LucideIcon, Ico, RotateCw, Plus, Minus, ChevronUp, ChevronDown,
  ChevronLeft, ChevronRight, Copy, Maximize2, Filter, FilterX, Info, X,
  SquarePen, TableProperties, Sigma, Undo2, FileCode, Code, Trash2, ArrowRight,
  Search, Download, ArrowUp, Check,
} from '../../icons'

// Transaction mode for the data grid. 'auto' commits each write immediately
// (the historical behaviour); 'manual' queues inserts/edits/deletes locally and
// commits them as one atomic transaction (server-side postgres.ExecScript).
type TxMode = 'auto' | 'manual'

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
  // Client-selectable page size (DataGrip-style), persisted as the default.
  const [size, setSize] = useState(loadPageSize)
  const [data, setData] = useState<GridResponse | null>(null)
  const [loading, setLoading] = useState(false)
  // Right-click menu (cell or table-level) and the modals it can open.
  const [menu, setMenu] = useState<{ x: number; y: number; items: MenuItem[] } | null>(null)
  // Cell inspector: a docked right-side panel (DataGrip-style) with Record /
  // Value / Aggregates tabs. `sel` is the cell it follows (set on single click);
  // `panel` is the open tab, or null when the inspector is hidden.
  const [sel, setSel] = useState<{ r: number; c: number } | null>(null)
  const [panel, setPanel] = useState<PanelTab | null>(null)
  const openPanel = (tab: PanelTab, r: number, c: number) => { setSel({ r, c }); setPanel(tab) }
  // Inline "add row" draft (DataGrip-style). `draft` maps a column index to the
  // value typed for it; columns absent from the map stay unset and render their
  // <default>/<null> placeholder. `editing` is the column index currently shown
  // as a text input. colMeta (notNull/default/autoInc, keyed by name) drives the
  // placeholder choice.
  const [draft, setDraft] = useState<Record<number, string> | null>(null)
  const [editing, setEditing] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [colMeta, setColMeta] = useState<Record<string, Column> | null>(null)
  // Inline edit of an existing cell (double-click / "Edit"): the {row,col} under
  // edit, and a flag while its UPDATE is in flight.
  const [edit, setEdit] = useState<{ r: number; c: number } | null>(null)
  const [savingCell, setSavingCell] = useState(false)
  // Transaction mode + the change set queued while in 'manual' mode. Edits are
  // keyed row index -> (col index -> new value) so multiple cells in one row
  // coalesce into a single UPDATE; deletes are a set of row indices; inserts are
  // the same draft maps submitDraft builds. All index into the current `rows`,
  // which is never reloaded until commit/rollback, so the indices stay valid.
  const [txMode, setTxMode] = useState<TxMode>('auto')
  const [pendingEdits, setPendingEdits] = useState<Record<number, Record<number, string>>>({})
  const [pendingDeletes, setPendingDeletes] = useState<Set<number>>(new Set())
  const [pendingInserts, setPendingInserts] = useState<Record<number, string>[]>([])
  const [committing, setCommitting] = useState(false)
  // Row selection (DataGrip-style): drag over the row-number column to range-
  // select, Ctrl/Cmd-click to toggle a single row, Shift-click to extend from the
  // anchor, and click the "#" header to (de)select all. Copy emits the selected
  // rows through the active "data extractor" (clipboard format).
  const [selRows, setSelRows] = useState<Set<number>>(new Set())
  const anchorRef = useRef<number | null>(null)
  const draggingRef = useRef(false)
  const [extractor, setExtractor] = useState<ExtractorId>(loadExtractor)

  const load = useCallback((p: number, w: string, o: string) => {
    setLoading(true)
    api.grid(connId, { schema, table, where: w, order: o, page: p, size })
      .then(setData).catch(e => setData({ result: null, readOnly: true, page: p, error: String(e.message || e) }))
      .finally(() => setLoading(false))
  }, [connId, schema, table, size])

  // Reloads on page or size change (changing size resets to page 0 below, so the
  // offset never lands past the end). `load` is in deps so a size change reruns.
  useEffect(() => { load(page, where, order) }, [load, page]) // eslint-disable-line react-hooks/exhaustive-deps

  // Column metadata for the inline editor's placeholders fetched once per table.
  useEffect(() => {
    setColMeta(null); setDraft(null); setEditing(null)
    setPendingEdits({}); setPendingDeletes(new Set()); setPendingInserts([])
    api.columns(connId, schema, table)
      .then(r => setColMeta(Object.fromEntries((r.columns || []).map(c => [c.name, c]))))
      .catch(() => setColMeta({}))
  }, [connId, schema, table])

  // Drop the selection whenever the page reloads (its indices would point at
  // stale rows otherwise), and end any drag-select on mouseup anywhere.
  useEffect(() => { setSelRows(new Set()); anchorRef.current = null }, [data])
  useEffect(() => {
    const up = () => { draggingRef.current = false }
    window.addEventListener('mouseup', up)
    return () => window.removeEventListener('mouseup', up)
  }, [])

  const apply = (e: React.FormEvent) => { e.preventDefault(); setPage(0); load(0, where, order) }
  const sort = (col: string, dir: 'asc' | 'desc') => { const o = `"${col}" ${dir}`; setOrder(o); setPage(0); load(0, where, o) }
  // Switching page size resets to the first page (the load effect reruns because
  // `size` is a `load` dependency) and remembers the choice as the new default.
  const changeSize = (n: number) => { savePageSize(n); setPage(0); setSize(n) }

  const result = data?.result
  const cols = result?.columns || []
  const rows = result?.rows || []
  const readOnly = data?.readOnly ?? (conn ? conn.readOnly || !app.caps.write : true)

  // Pagination math. We don't run a COUNT(*), so the total is only known once a
  // page comes back short: a full page (rows === size) means more rows may exist,
  // so we show "of N+" and allow Next; a short page is the exact end, so Next is
  // hidden and the total is exact.
  const hasNext = rows.length === size
  const first = rows.length ? page * size + 1 : 0
  const last = page * size + rows.length
  const totalLabel = hasNext ? `${last}+` : `${last}`

  // Autocomplete pools for the WHERE / ORDER BY filters. Columns come from the
  // table's metadata (fetched once, available before any rows load); falling back
  // to the result columns. Each pool appends the SQL keywords useful in that slot.
  const colList: Column[] = useMemo(
    () => colMeta ? Object.values(colMeta) : cols.map(name => ({ name, type: '' } as Column)),
    [colMeta, cols])
  // ORDER BY just needs the bare column name.
  const orderColSuggest = useMemo<Suggestion[]>(
    () => colList.map(c => ({ label: c.name, insert: qIdent(c.name), kind: 'col', detail: c.type || undefined })),
    [colList])
  // WHERE scaffolds the comparison from the column type: numeric/bool get
  // `col = ` (caret at the end); text/date/uuid/json get `col = ''` with the
  // caret parked between the quotes, ready for the value.
  const whereColSuggest = useMemo<Suggestion[]>(() => colList.map(c => {
    const name = qIdent(c.name)
    const t = (c.type || '').toLowerCase()
    const numeric = /int|numeric|decimal|real|double|money|serial|float|bool|bit/.test(t)
    const insert = numeric ? `${name} = ` : `${name} = ''`
    return { label: c.name, insert, kind: 'col', detail: c.type || undefined, caret: numeric ? undefined : insert.length - 1 }
  }), [colList])
  const whereCandidates = useMemo<Suggestion[]>(() => [
    ...whereColSuggest,
    ...['AND', 'OR', 'NOT', 'IS NULL', 'IS NOT NULL', 'LIKE', 'ILIKE', 'IN', 'BETWEEN', 'true', 'false', 'null']
      .map(k => ({ label: k, kind: 'kw' })),
  ], [whereColSuggest])
  const orderCandidates = useMemo<Suggestion[]>(() => [
    ...orderColSuggest,
    ...['ASC', 'DESC', 'NULLS FIRST', 'NULLS LAST'].map(k => ({ label: k, kind: 'kw' })),
  ], [orderColSuggest])

  // helpers shared by the menus. Quoting is engine-aware so the generated SQL
  // (inline edits, deletes, and the data extractors) matches the target dialect:
  // MySQL/MariaDB quote identifiers with backticks and treat backslash as an
  // escape inside string literals; Postgres & friends use ANSI double quotes and
  // (with standard_conforming_strings) take backslashes literally.
  const mysql = kindEngine(conn?.kind || 'postgres') === 'mysql'
  const qq = (s: string) => mysql ? '`' + s.replace(/`/g, '``') + '`' : '"' + s.replace(/"/g, '""') + '"'
  const lit = (s: string) => mysql
    ? "'" + s.replace(/\\/g, '\\\\').replace(/'/g, "''") + "'"
    : "'" + s.replace(/'/g, "''") + "'"
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
  const tableTsv = () => [cols.join('\t'), ...rows.map(r => r.join('\t'))].join('\n')
  // ── data extractors (clipboard formats) ──
  // One INSERT statement carrying every selected row as its own VALUES tuple.
  const sqlInsertMulti = (idx: number[]) =>
    `INSERT INTO ${qual} (${cols.map(qq).join(', ')}) VALUES\n` +
    idx.map(r => '  (' + rows[r].map(v => v === '' ? 'NULL' : lit(v)).join(', ') + ')').join(',\n') + ';'
  // One UPDATE per row, setting every column, targeting the row by its full
  // current contents (the grid tracks no primary key, same as the edit/delete path).
  const sqlUpdate = (r: number) =>
    `UPDATE ${qual}\nSET ${cols.map((c, i) => `${qq(c)} = ${cellLit(rows[r][i])}`).join(', ')}\nWHERE ${rowWhere(r)};`
  // Generic delimited (CSV family): a field is quoted only when it contains the
  // delimiter, a quote, or a newline, doubling any embedded quote.
  const delimCell = (s: string, sep: string) =>
    s.includes(sep) || s.includes('"') || s.includes('\n') ? '"' + s.replace(/"/g, '""') + '"' : s
  const delimited = (idx: number[], sep: string) =>
    [cols.map(c => delimCell(c, sep)).join(sep), ...idx.map(r => rows[r].map(c => delimCell(c, sep)).join(sep))].join('\n')
  const mdCell = (s: string) => s.replace(/\|/g, '\\|').replace(/\n/g, ' ')
  const mdTable = (idx: number[]) => [
    `| ${cols.join(' | ')} |`,
    `| ${cols.map(() => '---').join(' | ')} |`,
    ...idx.map(r => `| ${rows[r].map(mdCell).join(' | ')} |`),
  ].join('\n')
  const xmlEsc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
  const xmlRows = (idx: number[]) =>
    '<rows>\n' + idx.map(r =>
      '  <row>\n' + cols.map((c, i) => `    <col name="${xmlEsc(c)}">${xmlEsc(rows[r][i])}</col>`).join('\n') + '\n  </row>',
    ).join('\n') + '\n</rows>'
  const htmlTable = (idx: number[]) =>
    '<table>\n  <thead>\n    <tr>' + cols.map(c => `<th>${xmlEsc(c)}</th>`).join('') + '</tr>\n  </thead>\n  <tbody>\n' +
    idx.map(r => '    <tr>' + rows[r].map(v => `<td>${xmlEsc(v)}</td>`).join('') + '</tr>').join('\n') +
    '\n  </tbody>\n</table>'
  // Render the given rows in the active extractor's format.
  const extractRows = (idx: number[]): string => {
    switch (extractor) {
      case 'sql-insert-multirow': return sqlInsertMulti(idx)
      case 'sql-updates': return idx.map(sqlUpdate).join('\n')
      case 'csv': return delimited(idx, ',')
      case 'tsv': return delimited(idx, '\t')
      case 'pipe': return delimited(idx, '|')
      case 'semicolon': return delimited(idx, ';')
      case 'json': return '[\n' + idx.map(r => '  ' + jsonRow(r)).join(',\n') + '\n]'
      case 'xml': return xmlRows(idx)
      case 'html': return htmlTable(idx)
      case 'markdown': return mdTable(idx)
      default: return idx.map(sqlInsert).join('\n') // sql-inserts
    }
  }

  // ── row selection + copy ──
  const rangeSet = (a: number, b: number) => {
    const [lo, hi] = a <= b ? [a, b] : [b, a]
    const s = new Set<number>()
    for (let i = lo; i <= hi; i++) s.add(i)
    return s
  }
  const selectedIdx = () => [...selRows].sort((a, b) => a - b)
  const allSelected = rows.length > 0 && selRows.size === rows.length
  const toggleAll = () => setSelRows(allSelected ? new Set() : new Set(rows.map((_, i) => i)))
  const rowMouseDown = (i: number, e: React.MouseEvent) => {
    if (e.button !== 0) return // left button only; right-click opens the menu
    if (e.ctrlKey || e.metaKey) {
      setSelRows(s => { const n = new Set(s); n.has(i) ? n.delete(i) : n.add(i); return n }); anchorRef.current = i
    } else if (e.shiftKey && anchorRef.current !== null) {
      setSelRows(rangeSet(anchorRef.current, i))
    } else {
      setSelRows(new Set([i])); anchorRef.current = i
    }
    draggingRef.current = true
  }
  const rowEnter = (i: number) => {
    if (draggingRef.current && anchorRef.current !== null) setSelRows(rangeSet(anchorRef.current, i))
  }
  const extractorLabel = EXTRACTORS.find(e => e.id === extractor)?.label ?? 'SQL Inserts'
  const changeExtractor = (id: ExtractorId) => { saveExtractor(id); setExtractor(id) }
  // Copy the given rows (falling back to the whole page when none are selected)
  // in the active extractor's format.
  const copyRows = (idx: number[]) => {
    const use = idx.length ? idx : rows.map((_, i) => i)
    if (use.length === 0) return
    app.copy(extractRows(use))
    app.notify(`copied ${use.length} row${use.length === 1 ? '' : 's'} as ${extractorLabel}`)
  }
  const copySelection = () => copyRows(selectedIdx())
  const copyRowOrSel = (r: number) => copyRows(selRows.size ? selectedIdx() : [r])
  // Ctrl/Cmd-C copies the selection via the extractor, unless a cell editor has
  // focus (then the browser's own copy applies).
  const onGridKey = (e: React.KeyboardEvent) => {
    if (!(e.ctrlKey || e.metaKey) || (e.key !== 'c' && e.key !== 'C')) return
    const tag = (e.target as HTMLElement).tagName
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
    if (selRows.size === 0) return
    e.preventDefault()
    copySelection()
  }

  // ── write actions ──
  // The grid is a browser, not an inline editor, so write actions seed a query
  // console with a runnable statement the user reviews and runs reusing the
  // console's confirm gate + audit trail (same pattern as "query table").
  const cellLit = (v: string) => (v === '' ? 'NULL' : lit(v))
  // Best-effort row identifier: AND of every column = its value. The grid doesn't
  // track the primary key, so this targets the clicked row by its full contents.
  const rowWhere = (r: number) =>
    cols.map((c, i) => rows[r][i] === '' ? `${qq(c)} IS NULL` : `${qq(c)} = ${lit(rows[r][i])}`).join('\n  AND ')
  const openSql = (key: string, title: string, sql: string) =>
    app.openTab({ key, title, icon: 'console', view: { type: 'console', connId, sql } })
  // Add row → inline draft (DataGrip-style), not a seeded console. The draft row
  // renders at the top of the grid; the user fills cells and submits.
  const insertRow = () => { if (!draft) { setDraft({}); setEditing(null) } }
  // Placeholder shown for an unset cell: columns with a default (or auto-increment)
  // get their default when omitted from the INSERT; everything else gets NULL.
  const placeholder = (col: string) => {
    const m = colMeta?.[col]
    return m && (m.autoInc || m.default) ? '<default>' : '<null>'
  }
  // Pick the HTML input flavour from the column's SQL type so each cell edits with
  // the right control (number spinner, date/time pickers, boolean dropdown). All
  // values are still emitted as SQL literals, which Postgres casts per column.
  const fieldType = (col: string): 'number' | 'date' | 'datetime-local' | 'time' | 'bool' | 'text' => {
    const t = (colMeta?.[col]?.type || '').toLowerCase()
    if (/int|numeric|decimal|real|double|money|serial/.test(t)) return 'number'
    if (t.includes('timestamp')) return 'datetime-local'   // before date/time: "timestamp" contains both
    if (t.includes('date')) return 'date'
    if (t.includes('time')) return 'time'
    if (t.includes('bool')) return 'bool'
    return 'text'
  }
  // Set/clear one draft cell; an empty value reverts the cell to its placeholder.
  const setCell = (j: number, value: string) => setDraft(d => {
    const next = { ...(d || {}) }
    if (value === '') delete next[j]; else next[j] = value
    return next
  })
  const cancelDraft = () => { setDraft(null); setEditing(null) }
  const submitDraft = () => {
    if (!draft) return
    // Manual mode: queue the draft as a pending insert instead of running it.
    if (txMode === 'manual') {
      setPendingInserts(p => [...p, draft]); setDraft(null); setEditing(null)
      app.notify('insert queued')
      return
    }
    const set = Object.keys(draft).map(Number).sort((a, b) => a - b)
    // Only the cells the user touched go into the INSERT; the rest are omitted so
    // Postgres applies each column's default (or NULL) matching the placeholders.
    const sql = set.length === 0
      ? `INSERT INTO ${qual} DEFAULT VALUES;`
      : `INSERT INTO ${qual} (${set.map(i => qq(cols[i])).join(', ')}) VALUES (${set.map(i => lit(draft[i])).join(', ')});`
    setSaving(true)
    api.query(connId, sql, true)
      .then(r => {
        if (r.error) { app.notify(r.error, 'error'); return }
        app.notify('row inserted'); setDraft(null); setEditing(null); load(page, where, order)
      })
      .catch(e => app.notify(String(e.message || e), 'error'))
      .finally(() => setSaving(false))
  }
  // Inline cell edit: commit a single cell with an UPDATE that targets the row by
  // its current contents (rowWhere the grid doesn't track a primary key). Run
  // confirmed automatically (the user opted in by editing) and audited like any
  // other write; on success we reload so computed/trigger columns stay accurate.
  const commitEdit = (r: number, c: number, value: string) => {
    setEdit(null)
    // Manual mode: stash the new value as a pending edit (or drop it when the
    // value is reverted to the original) and update the cell display locally.
    if (txMode === 'manual') {
      const original = rows[r]?.[c] ?? ''
      setPendingEdits(p => {
        const row = { ...(p[r] || {}) }
        if (value === original) delete row[c]; else row[c] = value
        const next = { ...p }
        if (Object.keys(row).length === 0) delete next[r]; else next[r] = row
        return next
      })
      return
    }
    if (value === (rows[r]?.[c] ?? '')) return // unchanged → no-op
    setSavingCell(true)
    api.query(connId, `UPDATE ${qual}\nSET ${qq(cols[c])} = ${cellLit(value)}\nWHERE ${rowWhere(r)};`, true)
      .then(res => {
        if (res.error) { app.notify(res.error, 'error'); return }
        app.notify('cell updated'); load(page, where, order)
      })
      .catch(e => app.notify(String(e.message || e), 'error'))
      .finally(() => setSavingCell(false))
  }
  const deleteRow = (r: number) => {
    // Manual mode: mark the row for deletion and drop any pending edits on it
    // (an UPDATE then DELETE of the same row in one tx would mis-target, since
    // the DELETE's WHERE matches the row's original, now-updated, contents).
    if (txMode === 'manual') {
      setPendingDeletes(p => new Set(p).add(r))
      setPendingEdits(p => { const next = { ...p }; delete next[r]; return next })
      return
    }
    openSql(
      `console:${connId}:delete:${schema}.${table}`, `delete · ${table}`,
      `DELETE FROM ${qual}\nWHERE ${rowWhere(r)};`)
  }
  const deleteFiltered = () => openSql(
    `console:${connId}:delete:${schema}.${table}`, `delete · ${table}`,
    `DELETE FROM ${qual}${whereSuffix || '\nWHERE /* add a condition */ false'};`)

  // ── manual transaction (Tx: Manual) ──
  // Rows with at least one surviving edit (a deleted row's edits are ignored).
  const editedRows = Object.keys(pendingEdits).map(Number)
    .filter(r => !pendingDeletes.has(r) && Object.keys(pendingEdits[r]).length > 0)
  const pendingCount = editedRows.length + pendingDeletes.size + pendingInserts.length
  const clearPending = () => {
    setPendingEdits({}); setPendingDeletes(new Set()); setPendingInserts([])
    setDraft(null); setEditing(null); setEdit(null)
  }
  // Build the ordered statement list for the queued change set: per-row UPDATEs
  // (coalesced cells), then DELETEs, then INSERTs. Each row op targets its row by
  // full original contents (rowWhere) since the grid tracks no primary key.
  const txStatements = (): string[] => {
    const out: string[] = []
    for (const r of editedRows) {
      const sets = Object.keys(pendingEdits[r]).map(Number)
        .map(c => `${qq(cols[c])} = ${cellLit(pendingEdits[r][c])}`)
      out.push(`UPDATE ${qual}\nSET ${sets.join(', ')}\nWHERE ${rowWhere(r)};`)
    }
    for (const r of pendingDeletes) out.push(`DELETE FROM ${qual}\nWHERE ${rowWhere(r)};`)
    for (const d of pendingInserts) {
      const set = Object.keys(d).map(Number).sort((a, b) => a - b)
      out.push(set.length === 0
        ? `INSERT INTO ${qual} DEFAULT VALUES;`
        : `INSERT INTO ${qual} (${set.map(i => qq(cols[i])).join(', ')}) VALUES (${set.map(i => lit(d[i])).join(', ')});`)
    }
    return out
  }
  // Switch modes. Refuse to leave manual mode while changes are queued so they
  // are never silently dropped; the user must commit or roll back first.
  const toggleTx = () => {
    if (txMode === 'auto') { setTxMode('manual'); return }
    if (pendingCount > 0) { app.notify('commit or roll back queued changes first', 'error'); return }
    setTxMode('auto')
  }
  const commitTx = async (confirm = false) => {
    const stmts = txStatements()
    if (stmts.length === 0) return
    setCommitting(true)
    try {
      const res = await api.execTx(connId, stmts, confirm)
      if (res.needConfirm) {
        const ok = await app.confirm({
          title: 'Confirm transaction',
          body: 'This batch contains a destructive statement (DROP/TRUNCATE or an unfiltered DELETE/UPDATE). Commit anyway?',
          buttons: [{ label: 'Commit', value: 'ok', variant: 'danger' }],
        })
        if (ok) await commitTx(true)
        return
      }
      const n = res.count ?? stmts.length
      app.notify(`committed ${n} change${n === 1 ? '' : 's'}`)
      clearPending(); load(page, where, order)
    } catch (e) {
      app.notify(String((e as Error).message || e), 'error')
    } finally {
      setCommitting(false)
    }
  }
  const rollbackTx = () => { clearPending(); app.notify('queued changes discarded') }

  const copySum = (col: string) =>
    api.query(connId, `SELECT sum(${qq(col)}) FROM ${qual}${whereSuffix}`, true)
      .then(r => {
        if (r.error) return app.notify(r.error, 'error')
        const v = r.result?.rows?.[0]?.[0]
        app.copy(v ?? ''); app.notify(`SUM(${col}) = ${v ?? '∅'}`)
      })
      .catch(e => app.notify(String(e.message || e), 'error'))

  const fullTextSearch = async () => {
    const term = await app.prompt({ title: 'Full-text search', body: 'Matches any column (cast to text).', placeholder: 'search term', submitLabel: 'Search' })
    if (!term) return
    // ILIKE / ::text are Postgres-only; MySQL casts with CAST(... AS CHAR) and its
    // LIKE is already case-insensitive on the common collations.
    const like = (c: string) => mysql
      ? `CAST(${qq(c)} AS CHAR) LIKE ${lit('%' + term + '%')}`
      : `${qq(c)}::text ILIKE ${lit('%' + term + '%')}`
    setFilter('(' + cols.map(like).join(' OR ') + ')')
  }

  // Unified right-click menu mirrors the DataGrip data-editor menu. One menu
  // serves both cell and table-area clicks: the cell-specific block only appears
  // when there's a row under the cursor (rows[r] exists); otherwise it collapses
  // to the table-level actions with a schema.table header. Write actions are
  // enabled only when the connection is writable (`!readOnly`); on a read-only
  // connection they stay greyed, exactly like DataGrip.
  function menuItems(r: number, c: number): MenuItem[] {
    // r < 0 is the sentinel for a table-area click (header / blank space) where
    // there's no cell under the cursor guard on that rather than rows[r], since
    // rows[0] always exists when the table has data and would wrongly read as a cell.
    const cell = r >= 0 && rows[r] !== undefined
    const col = cols[c] ?? ''
    const val = rows[r]?.[c] ?? ''
    const eq = val === '' ? `${qq(col)} IS NULL` : `${qq(col)} = ${lit(val)}`
    const neq = val === '' ? `${qq(col)} IS NOT NULL` : `${qq(col)} <> ${lit(val)}`
    return [
      { head: cell ? col : `${schema}.${table}` },
      // ── cell-specific actions: need a row + column under the cursor ──
      ...(cell ? [
        { label: 'Edit', Icon: SquarePen, key: 'F2', disabled: readOnly, run: () => setEdit({ r, c }) },
        { label: 'Show record view', Icon: TableProperties, run: () => openPanel('record', r, c) },
        { label: 'Open in value editor', Icon: Maximize2, run: () => openPanel('value', r, c) },
        { label: 'Show aggregate view', Icon: Sigma, run: () => openPanel('aggregates', r, c) },
        { sep: true },
        { label: 'Revert selected', Icon: Undo2, disabled: true },
        { label: selRows.size > 1 ? `Copy ${selRows.size} rows as ${extractorLabel}` : `Copy row as ${extractorLabel}`, Icon: FileCode, key: 'Ctrl+C', run: () => copyRowOrSel(r) },
        { label: 'Change data extractor', Icon: Code,
          children: EXTRACTORS.map(ex => ({ label: (ex.id === extractor ? '✓ ' : '') + ex.label, run: () => changeExtractor(ex.id) })) },
        { label: 'Copy aggregation result (SUM)', Icon: Sigma, key: 'Ctrl+Shift+C', run: () => copySum(col) },
        { sep: true },
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
      // ── row / pagination actions: cell-agnostic, so shown in both contexts ──
      { label: 'Add row', Icon: Plus, key: 'Alt+Ins', disabled: readOnly, run: insertRow },
      { label: cell ? 'Delete row' : 'Delete rows', Icon: Trash2, key: 'Ctrl+Y', disabled: readOnly, run: () => cell ? deleteRow(r) : deleteFiltered() },
      { sep: true },
      { label: 'Go to', Icon: ArrowRight, children: [
        { label: 'First page', disabled: page === 0, run: () => setPage(0) },
        { label: 'Previous page', disabled: page === 0, run: () => setPage(p => Math.max(0, p - 1)) },
        { label: 'Next page', disabled: !hasNext, run: () => setPage(p => p + 1) },
        { label: 'Refresh', run: () => load(page, where, order) },
      ] },
      { sep: true },
      // ── table-wide actions: always shown ──
      { label: 'Refresh', Icon: RotateCw, run: () => load(page, where, order) },
      { sep: true },
      { label: 'Copy column names', Icon: Copy, run: () => app.copy(cols.join('\t')) },
      { label: selRows.size ? `Copy ${selRows.size} selected rows as ${extractorLabel}` : `Copy page as ${extractorLabel}`, Icon: FileCode, run: copySelection },
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
        <button className="tb-ico" title={readOnly ? 'add row (read-only)' : 'add row'} disabled={readOnly || !!draft} onClick={insertRow}><Plus size={16} /></button>
        <button className="tb-ico" title={readOnly ? 'remove rows (read-only)' : 'remove rows'} disabled={readOnly} onClick={deleteFiltered}><Minus size={16} /></button>
        {draft && <>
          <span className="tb-sep" />
          <button className="tb-ico ok" title="submit new row" disabled={saving} onClick={submitDraft}><ArrowUp size={16} /></button>
          <button className="tb-ico" title="discard new row" disabled={saving} onClick={cancelDraft}><X size={16} /></button>
        </>}
        <span className="tb-sep" />
        <button type="button" className={`tb-chip hud-label tx-chip${txMode === 'manual' ? ' manual' : ''}`}
          disabled={readOnly}
          title={txMode === 'manual'
            ? 'Manual commit: queue changes and commit/roll back as one transaction. Click to switch to auto-commit.'
            : 'Auto-commit: each change is applied immediately. Click to switch to manual transactions.'}
          onClick={toggleTx}>
          Tx: {txMode === 'manual' ? 'Manual' : 'Auto'}{txMode === 'manual' && pendingCount > 0 ? ` · ${pendingCount}` : ''}
        </button>
        {txMode === 'manual' && <>
          <button className="tb-ico ok" title={`commit ${pendingCount} change(s)`}
            disabled={pendingCount === 0 || committing} onClick={() => commitTx()}><Check size={16} /></button>
          <button className="tb-ico" title="roll back queued changes"
            disabled={pendingCount === 0 || committing} onClick={rollbackTx}><Undo2 size={16} /></button>
        </>}
        <span className="tb-sep" />
        <button className="tb-ico" disabled={rows.length === 0}
          title={`copy ${selRows.size ? `${selRows.size} selected rows` : 'page'} as ${extractorLabel}`}
          onClick={copySelection}><Copy size={16} /></button>
        <ExtractorMenu value={extractor} label={extractorLabel} onPick={changeExtractor} />
        <span className="tb-grow" />
        {selRows.size > 0 && <span className="tb-chip hud-label sel-chip">{selRows.size} selected</span>}
        {readOnly && <span className="ro">READ-ONLY</span>}
        {conn && <span className="tb-chip conn-chip hud-label" title={`${conn.kind}@${conn.host}`}>{conn.kind}@{conn.host}</span>}
      </div>

      <form className="filter-bar" onSubmit={apply}>
        <span className="fb-key hud-label">WHERE</span>
        <CodeField as="input" className="fb-input code" value={where} onChange={setWhere}
          candidates={whereCandidates} placeholder="user_id = 4 and group_code = 0" />
        <span className="fb-key hud-label">ORDER BY</span>
        <CodeField as="input" className="fb-input code" value={order} onChange={setOrder}
          candidates={orderCandidates} placeholder="event_time desc" />
        <button className="hud-btn-accent sm" type="submit">apply</button>
      </form>

      <div className="grid-split">
      <div className="grid-left">
      <div className="grid-body" tabIndex={0} onKeyDown={onGridKey}
        onContextMenu={e => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY, items: menuItems(-1, -1) }) }}>
        {data?.error ? <div className="alert error code">{data.error}</div>
          : !result ? <p className="dim">{loading ? 'loading…' : ''}</p>
          : !result.isSelect ? <p className="ok hud-label">{result.command} · {result.rowsAffected} rows affected · {result.duration}</p>
          : rows.length === 0 && !draft ? <p className="dim">0 rows · {result.duration}</p>
          : (
            <>
              <div className="tablewrap grid-table">
                <table className="data">
                  <thead><tr>
                    <th className="rownum" onClick={toggleAll}
                      title={allSelected ? 'clear selection' : 'select all rows'}>#</th>
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
                    {draft && (
                      <tr className="draft-row">
                        <td className="rownum">+</td>
                        {cols.map((c, j) => {
                          const v = draft[j]
                          return (
                            <td key={j} className="code draft-cell" onClick={() => setEditing(j)}>
                              {editing === j ? (
                                <DraftField type={fieldType(c)} value={v}
                                  onChange={val => setCell(j, val)}
                                  onCancel={() => setEditing(null)}
                                  // Enter advances to the next cell; the toolbar's ↑ submits the row.
                                  onNext={() => setEditing(j + 1 < cols.length ? j + 1 : null)} />
                              ) : v !== undefined ? v : <span className="draft-ph dim">{placeholder(c)}</span>}
                            </td>
                          )
                        })}
                      </tr>
                    )}
                    {/* Pending inserts (manual tx): rendered at the top like the live draft. */}
                    {pendingInserts.map((d, idx) => (
                      <tr key={`pi${idx}`} className="row-pending-ins">
                        <td className="rownum" title="queued insert">+</td>
                        {cols.map((c, j) => (
                          <td key={j} className="code">
                            {d[j] !== undefined ? d[j] : <span className="draft-ph dim">{placeholder(c)}</span>}
                          </td>
                        ))}
                      </tr>
                    ))}
                    {rows.map((row, i) => {
                      const del = pendingDeletes.has(i)
                      const redits = pendingEdits[i]
                      return (
                      <tr key={i} className={`${del ? 'row-pending-del' : ''}${selRows.has(i) ? ' row-sel' : ''}`}
                        onMouseEnter={() => rowEnter(i)}><td className="rownum" onMouseDown={e => rowMouseDown(i, e)}>{i + 1}</td>{row.map((v, j) => {
                        const pend = redits?.[j]
                        const display = pend !== undefined ? pend : v
                        return (
                        <td key={j} className={`code${!readOnly ? ' editable-cell' : ''}${pend !== undefined ? ' cell-dirty' : ''}${sel && sel.r === i && sel.c === j ? ' cell-sel' : ''}`}
                          onClick={() => setSel({ r: i, c: j })}
                          onDoubleClick={() => { if (!readOnly && !savingCell && !del) setEdit({ r: i, c: j }) }}
                          onContextMenu={e => { e.preventDefault(); e.stopPropagation(); setMenu({ x: e.clientX, y: e.clientY, items: menuItems(i, j) }) }}>
                          {edit && edit.r === i && edit.c === j
                            ? <CellEditor initial={display} onCommit={val => commitEdit(i, j, val)} onCancel={() => setEdit(null)} />
                            : display}
                        </td>
                      )})}</tr>
                    )})}
                  </tbody>
                </table>
              </div>
              <p className="grid-meta hud-label dim">{rows.length} rows{result.truncated ? ' · truncated at 1000' : ''} · {result.duration}{pendingCount > 0 ? ` · ${pendingCount} change(s) queued` : ''}</p>
            </>
          )}
      </div>

      <div className="grid-footer hud-label">
        {conn && <Ico name={conn.kind} className="gf-ico" />}
        <span className="gf-loc">{schema}.{table}</span>
        <span className="tb-grow" />
        <PageSizeMenu size={size} onPick={changeSize} />
        <span className="pg-range" title="rows on this page (total unknown until the last page)">
          {first === last ? `${last}` : `${first}-${last}`} of {totalLabel}
        </span>
        {page > 0 && <a className="pg-btn" onClick={() => setPage(p => p - 1)}><ChevronLeft size={14} /> prev</a>}
        <span className="gf-page">page {page + 1}</span>
        {hasNext && <a className="pg-btn" onClick={() => setPage(p => p + 1)}>next <ChevronRight size={14} /></a>}
      </div>
      </div>{/* grid-left */}

      {panel && sel && (
        <InspectorPanel
          tab={panel} onTab={setPanel} onClose={() => setPanel(null)}
          connId={connId} cols={cols} row={rows[sel.r] || []}
          col={cols[sel.c] ?? ''} value={rows[sel.r]?.[sel.c] ?? ''}
          aggSql={`SELECT count(*) AS "rows", count(${qq(cols[sel.c] ?? '')}) AS "non null", count(DISTINCT ${qq(cols[sel.c] ?? '')}) AS "distinct", min(${qq(cols[sel.c] ?? '')}) AS "min", max(${qq(cols[sel.c] ?? '')}) AS "max" FROM ${qual}${whereSuffix}`}
        />
      )}
      </div>{/* grid-split */}

      {menu && <CellMenu x={menu.x} y={menu.y} items={menu.items} onClose={() => setMenu(null)} />}
    </div>
  )
}

type PanelTab = 'record' | 'value' | 'aggregates'

const MW = 260, MH = 380

// Selectable grid page sizes. The server caps any page at 1000 rows (Query's
// row cap), so 1000 is the largest honest option, there is no unbounded "All".
const PAGE_SIZES = [10, 100, 250, 500, 1000]
const PAGE_SIZE_KEY = 'verix.grid.pageSize'
const loadPageSize = (): number => {
  const n = Number(localStorage.getItem(PAGE_SIZE_KEY))
  return PAGE_SIZES.includes(n) ? n : 100
}
const savePageSize = (n: number) => { try { localStorage.setItem(PAGE_SIZE_KEY, String(n)) } catch { /* ignore */ } }

// Data extractors: the clipboard format Copy uses (toolbar button, Ctrl+C, and
// the grid menu). Mirrors DataGrip's extractor picker; the choice persists.
const EXTRACTORS = [
  { id: 'sql-inserts', label: 'SQL Inserts', group: 'SQL' },
  { id: 'sql-insert-multirow', label: 'SQL Insert (multirow)', group: 'SQL' },
  { id: 'sql-updates', label: 'SQL Updates', group: 'SQL' },
  { id: 'csv', label: 'CSV', group: 'Delimited' },
  { id: 'tsv', label: 'TSV', group: 'Delimited' },
  { id: 'pipe', label: 'Pipe-separated', group: 'Delimited' },
  { id: 'semicolon', label: 'Semicolon-separated', group: 'Delimited' },
  { id: 'json', label: 'JSON', group: 'Structured' },
  { id: 'xml', label: 'XML', group: 'Structured' },
  { id: 'html', label: 'HTML', group: 'Structured' },
  { id: 'markdown', label: 'Markdown', group: 'Structured' },
] as const
type ExtractorId = typeof EXTRACTORS[number]['id']
const EXTRACTOR_GROUPS = ['SQL', 'Delimited', 'Structured'] as const
const EXTRACTOR_KEY = 'verix.grid.extractor'
const loadExtractor = (): ExtractorId => {
  const v = localStorage.getItem(EXTRACTOR_KEY)
  return EXTRACTORS.some(e => e.id === v) ? (v as ExtractorId) : 'sql-inserts'
}
const saveExtractor = (id: ExtractorId) => { try { localStorage.setItem(EXTRACTOR_KEY, id) } catch { /* ignore */ } }

// Toolbar dropdown that picks the active data extractor. Opens downward and
// reuses the shared .menu styling.
function ExtractorMenu({ value, label, onPick }: { value: ExtractorId; label: string; onPick: (id: ExtractorId) => void }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: Event) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    window.addEventListener('mousedown', onDown, true)
    window.addEventListener('keydown', onKey)
    return () => { window.removeEventListener('mousedown', onDown, true); window.removeEventListener('keydown', onKey) }
  }, [open])
  return (
    <div className="menu-wrap extractor-menu" ref={ref}>
      <button type="button" className="tb-chip hud-label" title="data extractor used by Copy" onClick={() => setOpen(o => !o)}>
        {label} <ChevronDown size={13} />
      </button>
      {open && (
        <div className="menu extractor-list">
          {EXTRACTOR_GROUPS.map(g => (
            <Fragment key={g}>
              <div className="ctx-head">{g}</div>
              {EXTRACTORS.filter(ex => ex.group === g).map(ex => (
                <button key={ex.id} type="button" className="menu-item" onClick={() => { onPick(ex.id); setOpen(false) }}>
                  <span className="mi-ico">{ex.id === value ? <Check size={14} /> : null}</span>
                  <span className="mi-label">{ex.label}</span>
                </button>
              ))}
            </Fragment>
          ))}
        </div>
      )}
    </div>
  )
}

// Footer page-size dropdown (opens upward). Picking a size reloads from page 1
// and persists the choice as the default for future grids.
function PageSizeMenu({ size, onPick }: { size: number; onPick: (n: number) => void }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: Event) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    window.addEventListener('mousedown', onDown, true)
    window.addEventListener('keydown', onKey)
    return () => { window.removeEventListener('mousedown', onDown, true); window.removeEventListener('keydown', onKey) }
  }, [open])
  return (
    <div className="pg-size" ref={ref}>
      <a className="pg-btn" title="rows per page" onClick={() => setOpen(o => !o)}>{size} / page <ChevronDown size={13} /></a>
      {open && (
        <div className="pg-size-menu">
          <div className="ctx-head">Page Size</div>
          {PAGE_SIZES.map(n => (
            <button key={n} type="button" className="menu-item" onClick={() => { onPick(n); setOpen(false) }}>
              <span className="mi-ico">{n === size ? <Check size={14} /> : null}</span>
              <span className="mi-label">{n}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// Right-click menu for grid cells / table area reuses the tree menu's styling
// and supports one level of fly-out submenus, keyboard-shortcut hints and
// disabled items (mirrors the DataGrip data-editor menu).
function CellMenu({ x, y, items, onClose }: { x: number; y: number; items: MenuItem[]; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)
  const [openSub, setOpenSub] = useState<number | null>(null)
  useEffect(() => {
    // Drop any page text selection so the OS selection handles/indicators don't
    // float on top of the menu (most visible as a bottom sheet on phones).
    window.getSelection()?.removeAllRanges()
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
  // Phones: render as a full-width bottom sheet (.ctx-menu.sheet) instead of a
  // positioned pop-up, so a long menu never overflows the narrow viewport.
  const sheet = window.matchMedia('(max-width: 900px)').matches
  const left = Math.max(8, Math.min(x, window.innerWidth - MW - 8))
  const top = Math.max(8, Math.min(y, window.innerHeight - MH - 8))
  const flip = left + MW + 220 > window.innerWidth
  const fire = (it: MenuItem) => { if (it.disabled || it.children) return; it.run?.(); onClose() }
  return (
    <>
      <div className="ctx-backdrop" onClick={onClose} onContextMenu={e => { e.preventDefault(); onClose() }} />
      <div ref={ref} className={`ctx-menu${sheet ? ' sheet' : ''}`} style={sheet ? undefined : { left, top }}>
        {items.map((it, i) => it.sep ? <div key={i} className="menu-sep" />
          : it.head ? <div key={i} className="ctx-head">{it.head}</div>
          : (
            <div key={i} className="menu-row">
              <button type="button" disabled={it.disabled}
                className={`menu-item${it.children ? ' has-children' : ''}${it.danger ? ' danger' : ''}${openSub === i ? ' expanded' : ''}`}
                onClick={() => it.children ? setOpenSub(s => s === i ? null : i) : fire(it)}>
                {it.Icon && <span className="mi-ico"><it.Icon size={15} /></span>}
                <span className="mi-label">{it.label}</span>
                {it.key && <span className="ctx-key">{it.key}</span>}
                {it.children && <span className="mi-caret"><ChevronRight size={13} /></span>}
              </button>
              {it.children && openSub === i && (
                <div className={`ctx-submenu${sheet ? ' sheet' : flip ? ' flip' : ''}`}>
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

// One editable cell of the inline "add row" draft. The control matches the
// column's SQL type number spinner, native date/time pickers, a boolean
// dropdown, or a plain text box. Enter commits + advances, Esc/blur commits.
function DraftField({ type, value, onChange, onCancel, onNext }: {
  type: 'number' | 'date' | 'datetime-local' | 'time' | 'bool' | 'text'
  value: string | undefined
  onChange: (v: string) => void
  onCancel: () => void
  onNext: () => void
}) {
  const ref = useRef<HTMLInputElement & HTMLSelectElement>(null)
  useEffect(() => { ref.current?.focus() }, [])
  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') { e.preventDefault(); onNext() }
    else if (e.key === 'Escape') { e.preventDefault(); onCancel() }
  }
  if (type === 'bool') return (
    <select ref={ref} className="draft-input code" value={value ?? ''} onKeyDown={onKeyDown}
      onBlur={onCancel} onChange={e => onChange(e.target.value)}>
      <option value="">&lt;default&gt;</option>
      <option value="true">true</option>
      <option value="false">false</option>
    </select>
  )
  return (
    <input ref={ref} className="draft-input code" value={value ?? ''}
      type={type === 'text' ? 'text' : type} step={type === 'number' ? 'any' : undefined}
      onKeyDown={onKeyDown} onBlur={onCancel} onChange={e => onChange(e.target.value)} />
  )
}

// Inline editor for an existing cell. A plain text input prefilled with the
// cell's current string: the grid stores every value as text and emits it as a
// SQL literal (Postgres casts per column), so a raw text box round-trips any
// type without the format-mismatch data-loss a typed date/number picker risks.
// Enter or blur commits; Esc cancels. A `done` guard stops the blur-after-Enter
// (or blur-after-Esc) from firing a second commit.
function CellEditor({ initial, onCommit, onCancel }: {
  initial: string; onCommit: (v: string) => void; onCancel: () => void
}) {
  const ref = useRef<HTMLInputElement>(null)
  const [v, setV] = useState(initial)
  const done = useRef(false)
  useEffect(() => { ref.current?.focus(); ref.current?.select() }, [])
  const commit = () => { if (done.current) return; done.current = true; onCommit(v) }
  const cancel = () => { if (done.current) return; done.current = true; onCancel() }
  return (
    <input ref={ref} className="draft-input code" value={v}
      onChange={e => setV(e.target.value)} onBlur={commit}
      onKeyDown={e => {
        if (e.key === 'Enter') { e.preventDefault(); commit() }
        else if (e.key === 'Escape') { e.preventDefault(); cancel() }
      }} />
  )
}

// InspectorPanel is the docked right-side cell inspector (DataGrip-style),
// replacing the old Record / Value / Aggregate modals. It follows the selected
// cell and switches between three tabs without leaving the grid.
function InspectorPanel({ tab, onTab, onClose, connId, cols, row, col, value, aggSql }: {
  tab: PanelTab; onTab: (t: PanelTab) => void; onClose: () => void
  connId: number; cols: string[]; row: string[]; col: string; value: string; aggSql: string
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  const TABS: { id: PanelTab; label: string }[] = [
    { id: 'record', label: 'Record' }, { id: 'value', label: 'Value' }, { id: 'aggregates', label: 'Aggregates' },
  ]
  return (
    <aside className="grid-side hud-panel">
      <div className="side-tabs">
        {TABS.map(t => (
          <button key={t.id} type="button" className={`side-tab${tab === t.id ? ' on' : ''}`}
            onClick={() => onTab(t.id)}>{t.label}</button>
        ))}
        <span className="tb-grow" />
        <button type="button" className="ico-btn" title="close inspector" onClick={onClose}><X size={15} /></button>
      </div>
      <div className="side-body">
        {tab === 'record' ? <RecordPanel cols={cols} row={row} />
          : tab === 'value' ? <ValuePanel col={col} value={value} />
          : <AggregatesPanel connId={connId} col={col} sql={aggSql} />}
      </div>
    </aside>
  )
}

// Record tab: the whole row laid out vertically as column → value.
function RecordPanel({ cols, row }: { cols: string[]; row: string[] }) {
  const app = useApp()
  const asJson = () => app.copy(JSON.stringify(Object.fromEntries(cols.map((c, i) => [c, row[i]])), null, 2))
  return (
    <>
      <div className="side-scroll">
        <table className="data record-view">
          <tbody>
            {cols.map((c, i) => (
              <tr key={i}>
                <td className="rv-key hud-label">{c}</td>
                <td className="code rv-val" onClick={() => app.copy(row[i] ?? '')} title="click to copy">
                  {row[i] === '' ? <span className="dim">null</span> : row[i]}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="side-foot">
        <span className="tb-grow" />
        <button type="button" className="hud-btn-accent sm" onClick={asJson}>Copy JSON</button>
      </div>
    </>
  )
}

// Value tab: the full cell value. When it parses as JSON it is pretty-printed
// and syntax-highlighted (DataGrip-style); a toggle falls back to the raw text.
function ValuePanel({ col, value }: { col: string; value: string }) {
  const app = useApp()
  const pretty = useMemo(() => tryFormatJson(value), [value])
  const [raw, setRaw] = useState(false)
  const showJson = pretty !== null && !raw
  return (
    <>
      <div className="side-valhead hud-label">
        <span className="dim">{col}</span>
        <span className="tb-grow" />
        {pretty !== null && (
          <button type="button" className={`mini-toggle${showJson ? ' on' : ''}`}
            title="toggle JSON formatting" onClick={() => setRaw(r => !r)}>
            <Code size={13} /> {showJson ? 'JSON' : 'Raw'}
          </button>
        )}
      </div>
      <div className="side-scroll">
        {value === '' ? <pre className="code value-viewer dim">(empty / null)</pre>
          : showJson
            ? <pre className="code value-viewer json-view" dangerouslySetInnerHTML={{ __html: highlightJson(pretty!) }} />
            : <pre className="code value-viewer">{value}</pre>}
      </div>
      <div className="side-foot">
        <span className="hud-label dim">{value.length} chars</span>
        <span className="tb-grow" />
        <button type="button" className="hud-btn-accent sm" onClick={() => app.copy(showJson ? pretty! : value)}>Copy</button>
      </div>
    </>
  )
}

// Aggregates tab: count / distinct / min / max for the selected column (honouring
// the current WHERE), recomputed whenever the column or filter changes.
function AggregatesPanel({ connId, col, sql }: { connId: number; col: string; sql: string }) {
  const [resp, setResp] = useState<QueryResponse | null>(null)
  useEffect(() => {
    setResp(null)
    api.query(connId, sql, true).then(setResp).catch(e => setResp({ readOnly: true, error: String(e.message || e) }))
  }, [connId, sql])
  const r = resp?.result
  return (
    <div className="side-scroll">
      <div className="side-valhead hud-label"><span className="dim">{col}</span></div>
      {!resp ? <p className="dim" style={{ padding: '.6rem' }}>computing…</p>
        : resp.error ? <div className="alert error code">{resp.error}</div>
        : r && r.rows?.[0] ? (
          <table className="data record-view">
            <tbody>
              {(r.columns || []).map((c, i) => (
                <tr key={i}><td className="rv-key hud-label">{c}</td><td className="code rv-val">{r.rows![0][i]}</td></tr>
              ))}
            </tbody>
          </table>
        ) : <p className="dim" style={{ padding: '.6rem' }}>no result</p>}
    </div>
  )
}

// tryFormatJson returns the value pretty-printed if it parses as a JSON object or
// array, else null (scalars and plain text are left to render verbatim).
function tryFormatJson(s: string): string | null {
  const t = s.trim()
  if (!t || (t[0] !== '{' && t[0] !== '[')) return null
  try { return JSON.stringify(JSON.parse(t), null, 2) } catch { return null }
}

// highlightJson wraps tokens of a pretty-printed JSON string in coloured spans.
// Input is HTML-escaped first, so the result is safe to inject.
function highlightJson(json: string): string {
  const esc = json.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  return esc.replace(
    /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false)\b|\bnull\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
    m => {
      let cls = 'j-num'
      if (/^"/.test(m)) cls = /:$/.test(m) ? 'j-key' : 'j-str'
      else if (m === 'true' || m === 'false') cls = 'j-bool'
      else if (m === 'null') cls = 'j-null'
      return `<span class="${cls}">${m}</span>`
    })
}
