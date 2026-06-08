// Data model + DDL generation for the DataGrip-style table designer.
//
// The designer edits a TableModel in React state; this module turns that model
// into the SQL that the preview shows and the server runs. "create" emits one
// CREATE TABLE (+ indexes/comment); "modify" diffs the edited model against the
// snapshot captured at load and emits only the ALTERs needed. SQL generation
// lives here (not in a component) so the preview and the executed statements are
// produced by exactly one code path — what you see is what runs.

import type { DocResponse } from './types'

let uidc = 0
export const uid = () => `n${++uidc}`

export interface Col {
  uid: string
  name: string
  type: string
  notNull: boolean
  def: string
  pk: boolean
  origPk?: boolean // was this column part of the primary key at load (modify mode)
  orig?: { name: string; type: string; notNull: boolean; def: string }
}
export interface Uniq {
  uid: string; name: string; cols: string
  orig?: { name: string; cols: string }
}
export interface FK {
  uid: string; name: string; cols: string; refTable: string; refCols: string; onDelete: string
  orig?: { name: string; cols: string; refTable: string; refCols: string; onDelete: string }
}
export interface Idx {
  uid: string; name: string; cols: string; unique: boolean
  orig?: { name: string; cols: string; unique: boolean }
}
export interface Chk {
  uid: string; name: string; expr: string
  orig?: { name: string; expr: string }
}

export interface Snapshot {
  pkName?: string
  comment: string
  colNames: string[]
  uniqueNames: string[]
  fkNames: string[]
  indexNames: string[]
  checkNames: string[]
}

export interface TableModel {
  name: string
  comment: string
  cols: Col[]
  uniques: Uniq[]
  fks: FK[]
  indexes: Idx[]
  checks: Chk[]
  origName?: string
  snapshot?: Snapshot
}

export const emptyModel = (): TableModel => ({
  name: '', comment: '', cols: [], uniques: [], fks: [], indexes: [], checks: [],
})

// ── quoting ──────────────────────────────────────────────────────────────────
export const qi = (s: string) => '"' + String(s).replace(/"/g, '""') + '"'
export const ql = (s: string) => "'" + String(s).replace(/'/g, "''") + "'"
const qual = (schema: string, t: string) => qi(schema) + '.' + qi(t)
// Comma-separated identifier list → quoted: `a, b ` → `"a", "b"`.
const idList = (s: string) => s.split(',').map(x => x.trim()).filter(Boolean).map(qi).join(', ')

function colDef(c: Col): string {
  let s = `${qi(c.name)} ${c.type.trim() || 'text'}`
  if (c.notNull) s += ' NOT NULL'
  if (c.def.trim()) s += ' DEFAULT ' + c.def.trim()
  return s
}
function fkClause(f: FK): string {
  let s = `CONSTRAINT ${qi(f.name)} FOREIGN KEY (${idList(f.cols)}) REFERENCES ${f.refTable.trim()} (${idList(f.refCols)})`
  if (f.onDelete) s += ' ON DELETE ' + f.onDelete
  return s
}
const pkName = (m: TableModel) => m.snapshot?.pkName || `${m.name}_pkey`

// ── CREATE ───────────────────────────────────────────────────────────────────
export function generateCreate(schema: string, m: TableModel): string[] {
  const t = qual(schema, m.name || 'table_name')
  const inner: string[] = m.cols.filter(c => c.name.trim()).map(colDef)
  const pkCols = m.cols.filter(c => c.pk && c.name.trim()).map(c => qi(c.name))
  if (pkCols.length) inner.push(`CONSTRAINT ${qi(pkName(m))} PRIMARY KEY (${pkCols.join(', ')})`)
  for (const u of m.uniques) if (u.cols.trim()) inner.push(`CONSTRAINT ${qi(u.name)} UNIQUE (${idList(u.cols)})`)
  for (const f of m.fks) if (f.cols.trim() && f.refTable.trim()) inner.push(fkClause(f))
  for (const c of m.checks) if (c.expr.trim()) inner.push(`CONSTRAINT ${qi(c.name)} CHECK (${c.expr.trim()})`)

  const body = inner.length ? `(\n  ${inner.join(',\n  ')}\n)` : `()`
  const stmts = [`CREATE TABLE ${t} ${body}`]
  for (const i of m.indexes) if (i.cols.trim())
    stmts.push(`CREATE ${i.unique ? 'UNIQUE ' : ''}INDEX ${qi(i.name)} ON ${t} (${i.cols.trim()})`)
  if (m.comment.trim()) stmts.push(`COMMENT ON TABLE ${t} IS ${ql(m.comment.trim())}`)
  return stmts
}

// ── MODIFY (diff against snapshot) ───────────────────────────────────────────
export function generateModify(schema: string, m: TableModel): string[] {
  const snap = m.snapshot!
  const t = qual(schema, m.origName!) // ALTER against the original name; rename comes last
  const out: string[] = []
  const alter = (body: string) => out.push(`ALTER TABLE ${t} ${body}`)

  // 1. add new columns
  for (const c of m.cols) if (!c.orig && c.name.trim()) alter(`ADD COLUMN ${colDef(c)}`)

  // 2. alter existing columns (rename first, then type/null/default by new name)
  for (const c of m.cols) {
    if (!c.orig) continue
    const o = c.orig
    if (c.name !== o.name) alter(`RENAME COLUMN ${qi(o.name)} TO ${qi(c.name)}`)
    if (c.type.trim() !== o.type.trim()) alter(`ALTER COLUMN ${qi(c.name)} TYPE ${c.type.trim()}`)
    if (c.notNull !== o.notNull) alter(`ALTER COLUMN ${qi(c.name)} ${c.notNull ? 'SET' : 'DROP'} NOT NULL`)
    if (c.def.trim() !== o.def.trim())
      alter(c.def.trim() ? `ALTER COLUMN ${qi(c.name)} SET DEFAULT ${c.def.trim()}` : `ALTER COLUMN ${qi(c.name)} DROP DEFAULT`)
  }

  // 3. drop constraints + indexes that were removed or changed (before col drops)
  const liveUniq = new Set(m.uniques.map(u => u.orig?.name).filter(Boolean))
  const liveFk = new Set(m.fks.map(f => f.orig?.name).filter(Boolean))
  const liveChk = new Set(m.checks.map(c => c.orig?.name).filter(Boolean))
  const liveIdx = new Set(m.indexes.map(i => i.orig?.name).filter(Boolean))

  const pkCols = m.cols.filter(c => c.pk && c.name.trim()).map(c => c.name)
  const pkChanged = pkColsChanged(m)
  if (pkChanged && snap.pkName) alter(`DROP CONSTRAINT ${qi(snap.pkName)}`)

  for (const name of snap.uniqueNames) if (!liveUniq.has(name)) alter(`DROP CONSTRAINT ${qi(name)}`)
  for (const u of m.uniques) if (u.orig && uniqChanged(u)) alter(`DROP CONSTRAINT ${qi(u.orig.name)}`)
  for (const name of snap.fkNames) if (!liveFk.has(name)) alter(`DROP CONSTRAINT ${qi(name)}`)
  for (const f of m.fks) if (f.orig && fkChanged(f)) alter(`DROP CONSTRAINT ${qi(f.orig.name)}`)
  for (const name of snap.checkNames) if (!liveChk.has(name)) alter(`DROP CONSTRAINT ${qi(name)}`)
  for (const c of m.checks) if (c.orig && chkChanged(c)) alter(`DROP CONSTRAINT ${qi(c.orig.name)}`)
  for (const name of snap.indexNames) if (!liveIdx.has(name)) out.push(`DROP INDEX ${qual(schema, name)}`)
  for (const i of m.indexes) if (i.orig && idxChanged(i)) out.push(`DROP INDEX ${qual(schema, i.orig.name)}`)

  // 4. drop removed columns
  const liveCol = new Set(m.cols.map(c => c.orig?.name).filter(Boolean))
  for (const name of snap.colNames) if (!liveCol.has(name)) alter(`DROP COLUMN ${qi(name)}`)

  // 5. add new / changed constraints + indexes
  if (pkChanged && pkCols.length)
    alter(`ADD CONSTRAINT ${qi(pkName(m))} PRIMARY KEY (${pkCols.map(qi).join(', ')})`)
  for (const u of m.uniques) if (u.cols.trim() && (!u.orig || uniqChanged(u)))
    alter(`ADD CONSTRAINT ${qi(u.name)} UNIQUE (${idList(u.cols)})`)
  for (const f of m.fks) if (f.cols.trim() && f.refTable.trim() && (!f.orig || fkChanged(f)))
    alter(`ADD ${fkClause(f)}`)
  for (const c of m.checks) if (c.expr.trim() && (!c.orig || chkChanged(c)))
    alter(`ADD CONSTRAINT ${qi(c.name)} CHECK (${c.expr.trim()})`)
  for (const i of m.indexes) if (i.cols.trim() && (!i.orig || idxChanged(i)))
    out.push(`CREATE ${i.unique ? 'UNIQUE ' : ''}INDEX ${qi(i.name)} ON ${t} (${i.cols.trim()})`)

  // 6. table comment
  if (m.comment.trim() !== snap.comment.trim())
    out.push(`COMMENT ON TABLE ${t} IS ${m.comment.trim() ? ql(m.comment.trim()) : 'NULL'}`)

  // 7. rename table (last, so every statement above used the original name)
  if (m.name.trim() && m.name !== m.origName) out.push(`ALTER TABLE ${t} RENAME TO ${qi(m.name)}`)

  return out
}

const uniqChanged = (u: Uniq) => !!u.orig && u.orig.cols.trim() !== u.cols.trim()
const chkChanged = (c: Chk) => !!c.orig && (c.orig.name !== c.name || c.orig.expr.trim() !== c.expr.trim())
const idxChanged = (i: Idx) => !!i.orig && (i.orig.name !== i.name || i.orig.cols.trim() !== i.cols.trim() || i.orig.unique !== i.unique)
const fkChanged = (f: FK) => !!f.orig && (
  f.orig.name !== f.name || f.orig.cols.trim() !== f.cols.trim() ||
  f.orig.refTable.trim() !== f.refTable.trim() || f.orig.refCols.trim() !== f.refCols.trim() ||
  f.orig.onDelete !== f.onDelete)

// Did the primary-key column set change vs the snapshot?
function pkColsChanged(m: TableModel): boolean {
  const cur = m.cols.filter(c => c.pk && c.name.trim()).map(c => c.name).sort().join(',')
  const orig = m.cols.filter(c => c.orig && c.origPk).map(c => c.orig!.name).sort().join(',')
  return cur !== orig
}

// loadModel builds a modify-mode model + snapshot from the doc endpoint payload.
export function loadModel(doc: DocResponse): TableModel {
  const keys = doc.keys || []
  const pk = keys.find(k => k.Type === 'primary')
  const pkSet = new Set((pk?.Cols || '').split(',').map(s => s.trim()).filter(Boolean))

  const cols: Col[] = doc.columns.map(c => {
    const type = c.typeText || c.type
    const isPk = pkSet.has(c.name)
    return {
      uid: uid(), name: c.name, type, notNull: c.notNull, def: c.default || '', pk: isPk,
      origPk: isPk,
      orig: { name: c.name, type, notNull: c.notNull, def: c.default || '' },
    }
  })

  const uniques: Uniq[] = keys.filter(k => k.Type === 'unique').map(k => ({
    uid: uid(), name: k.Name, cols: k.Cols || '', orig: { name: k.Name, cols: k.Cols || '' },
  }))
  const fks: FK[] = keys.filter(k => k.Type === 'foreign').map(k => {
    const ref = parseFK(k.Def)
    const f = { uid: uid(), name: k.Name, cols: k.Cols || '', refTable: ref.table, refCols: ref.cols, onDelete: ref.onDelete }
    return { ...f, orig: { name: f.name, cols: f.cols, refTable: f.refTable, refCols: f.refCols, onDelete: f.onDelete } }
  })
  const checks: Chk[] = keys.filter(k => k.Type === 'check').map(k => {
    const expr = parseCheck(k.Def)
    return { uid: uid(), name: k.Name, expr, orig: { name: k.Name, expr } }
  })

  // Skip indexes that merely back a PK/unique constraint (managed via those).
  const backed = new Set<string>([pk?.Name, ...uniques.map(u => u.name)].filter(Boolean) as string[])
  const indexes: Idx[] = (doc.indexes || [])
    .filter(i => !i.Primary && !backed.has(i.Name))
    .map(i => ({
      uid: uid(), name: i.Name, cols: i.Cols || '', unique: i.Unique,
      orig: { name: i.Name, cols: i.Cols || '', unique: i.Unique },
    }))

  return {
    name: doc.table, origName: doc.table, comment: doc.comment || '',
    cols, uniques, fks, indexes, checks,
    snapshot: {
      pkName: pk?.Name, comment: doc.comment || '',
      colNames: doc.columns.map(c => c.name),
      uniqueNames: uniques.map(u => u.name),
      fkNames: fks.map(f => f.name),
      indexNames: indexes.map(i => i.name),
      checkNames: checks.map(c => c.name),
    },
  }
}

function parseFK(def: string): { table: string; cols: string; onDelete: string } {
  const m = /REFERENCES\s+([^\s(]+)\s*\(([^)]*)\)/i.exec(def || '')
  const od = /ON DELETE\s+(CASCADE|SET NULL|SET DEFAULT|RESTRICT|NO ACTION)/i.exec(def || '')
  const cols = (m?.[2] || '').split(',').map(s => s.trim().replace(/^"|"$/g, '')).join(', ')
  return { table: m?.[1] || '', cols, onDelete: od ? od[1].toUpperCase() : '' }
}
function parseCheck(def: string): string {
  const m = /^\s*CHECK\s*\((.*)\)\s*$/is.exec(def || '')
  return (m?.[1] || def || '').trim()
}
