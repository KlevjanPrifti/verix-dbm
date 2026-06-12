import { useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { Column, QueryResponse, Schema } from '../../types'
import CodeField, { qIdent, type Suggestion } from '../Autocomplete'
import ResultTable from '../ResultTable'
import { Play } from '../../icons'

// SQL keywords offered everywhere in the console. Multi-word entries insert as a
// unit so e.g. "del" -> "DELETE FROM".
const SQL_KEYWORDS = [
  'SELECT', 'FROM', 'WHERE', 'INSERT INTO', 'UPDATE', 'DELETE FROM', 'SET', 'VALUES',
  'JOIN', 'LEFT JOIN', 'INNER JOIN', 'ON', 'GROUP BY', 'ORDER BY', 'HAVING', 'LIMIT',
  'OFFSET', 'DISTINCT', 'AS', 'AND', 'OR', 'NOT', 'NULL', 'IS NULL', 'IS NOT NULL',
  'LIKE', 'ILIKE', 'IN', 'BETWEEN', 'ASC', 'DESC', 'RETURNING', 'COUNT(*)',
]

// Tables referenced after FROM / JOIN / UPDATE / INTO, so the console can fetch
// just those tables' columns for column suggestions.
const TABLE_REF_RE = /\b(?:from|join|update|into)\s+("?[\w.]+"?)/gi

// Reserved words coloured as keywords by the highlighter. Kept broad so common
// SQL reads well without trying to be an exhaustive Postgres grammar.
const KW = new Set([
  'select', 'from', 'where', 'insert', 'into', 'update', 'delete', 'set', 'values',
  'join', 'left', 'right', 'inner', 'outer', 'full', 'cross', 'on', 'using', 'natural',
  'group', 'by', 'order', 'having', 'limit', 'offset', 'distinct', 'as', 'and', 'or',
  'not', 'null', 'is', 'like', 'ilike', 'in', 'between', 'asc', 'desc', 'returning',
  'union', 'all', 'intersect', 'except', 'case', 'when', 'then', 'else', 'end', 'exists',
  'create', 'table', 'view', 'index', 'drop', 'alter', 'add', 'column', 'constraint',
  'primary', 'foreign', 'key', 'references', 'unique', 'default', 'check', 'cascade',
  'truncate', 'with', 'recursive', 'begin', 'commit', 'rollback', 'grant', 'revoke',
  'true', 'false', 'over', 'partition', 'window', 'filter', 'cast', 'array', 'any',
])

// Token regex (ordered): comments, strings, quoted idents, numbers, words, punctuation.
const TOK_RE = /(--[^\n]*|\/\*[\s\S]*?\*\/)|('(?:[^']|'')*'?)|("(?:[^"]|"")*"?)|(\b\d+(?:\.\d+)?\b)|([A-Za-z_][A-Za-z0-9_]*)|([(),.;*=<>!+\-/%|]+)/g

const escHtml = (s: string) => s.replace(/[&<>]/g, c => (c === '&' ? '&amp;' : c === '<' ? '&lt;' : '&gt;'))

// Tokenise SQL into an HTML string of coloured <span>s for the console overlay.
// Returning a string (vs React nodes) keeps each keystroke to one innerHTML write
// instead of reconciling hundreds of elements.
function highlightSQL(sql: string): string {
  let out = '', last = 0
  for (const m of sql.matchAll(TOK_RE)) {
    const i = m.index!
    if (i > last) out += escHtml(sql.slice(last, i))
    const [tok, comment, str, ident, num, word] = m
    let cls = 'tk-punct'
    if (comment) cls = 'tk-com'
    else if (str) cls = 'tk-str'
    else if (ident) cls = 'tk-id'
    else if (num) cls = 'tk-num'
    else if (word) cls = KW.has(word.toLowerCase()) ? 'tk-kw' : 'tk-word'
    out += `<span class="${cls}">${escHtml(tok)}</span>`
    last = i + tok.length
  }
  if (last < sql.length) out += escHtml(sql.slice(last))
  return out
}

function resolveRef(ref: string, schemas: Schema[]): { schema: string; table: string; key: string } | null {
  const parts = ref.replace(/"/g, '').split('.')
  if (parts.length === 2) return { schema: parts[0], table: parts[1], key: `${parts[0]}.${parts[1]}` }
  const table = parts[0]
  // Bare name: find its schema in the explorer tree (default to public).
  for (const s of schemas) for (const t of s.Tables || []) if (t.Name === table) return { schema: s.Name, table, key: `${s.Name}.${table}` }
  return { schema: 'public', table, key: `public.${table}` }
}

// Postgres query console: run SQL with a confirmation gate for destructive
// statements, plus identifier autocomplete (keywords, tables, and columns of
// referenced tables).
export default function ConsoleTab({ connId, initialSql, schema }: { connId: number; initialSql?: string; schema?: string }) {
  const app = useApp()
  const conn = app.connById(connId)
  const [sql, setSql] = useState(initialSql ?? '')
  const [resp, setResp] = useState<QueryResponse | null>(null)
  const [running, setRunning] = useState(false)
  const readOnly = conn ? conn.readOnly || !app.caps.write : false

  // Schema tree (table names) and a lazily-filled cache of columns keyed by
  // schema.table. colVer bumps to refresh suggestions when the cache grows.
  const [schemas, setSchemas] = useState<Schema[]>([])
  const colCache = useRef<Record<string, Column[]>>({})
  const [colVer, setColVer] = useState(0)

  useEffect(() => { api.explorer(connId).then(d => setSchemas(d.schemas || [])).catch(() => {}) }, [connId])

  // Fetch columns for any newly-referenced table (once per table).
  useEffect(() => {
    for (const m of sql.matchAll(TABLE_REF_RE)) {
      const r = resolveRef(m[1], schemas)
      if (!r || r.key in colCache.current) continue
      colCache.current[r.key] = [] // reserve to avoid refetch while in flight
      api.columns(connId, r.schema, r.table)
        .then(res => { colCache.current[r.key] = res.columns || []; setColVer(v => v + 1) })
        .catch(() => {})
    }
  }, [sql, schemas, connId])

  const candidates = useMemo<Suggestion[]>(() => {
    const out: Suggestion[] = []
    for (const s of schemas) for (const t of s.Tables || []) {
      out.push({ label: t.Name, insert: qIdent(t.Name), kind: 'table', detail: s.Name })
      if (s.Name !== 'public') out.push({ label: `${s.Name}.${t.Name}`, insert: `${qIdent(s.Name)}.${qIdent(t.Name)}`, kind: 'table' })
    }
    const seen = new Set<string>()
    for (const [key, cols] of Object.entries(colCache.current)) for (const c of cols) {
      if (seen.has(c.name)) continue
      seen.add(c.name)
      out.push({ label: c.name, insert: qIdent(c.name), kind: 'col', detail: c.type || key.split('.').pop() })
    }
    for (const k of SQL_KEYWORDS) out.push({ label: k, kind: 'kw' })
    return out
    // colVer participates so suggestions refresh as column fetches resolve.
  }, [schemas, colVer]) // eslint-disable-line react-hooks/exhaustive-deps

  const run = (confirm = false) => {
    setRunning(true)
    api.query(connId, sql, confirm, schema).then(setResp)
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
        <CodeField as="textarea" className="hud-input code console-editor" value={sql} onChange={setSql}
          candidates={candidates} onKeyDown={onKey} highlight={highlightSQL} placeholder="select * from … limit 100;" />
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
