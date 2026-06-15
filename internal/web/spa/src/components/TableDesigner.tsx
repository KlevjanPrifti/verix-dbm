import { useEffect, useMemo, useState } from 'react'
import { api } from '../api'
import { useApp, type TableDesignerParams } from '../appctx'
import {
  type LucideIcon, Ico, X, Minus, ArrowUp, ArrowDown, Plus, KeyRound, Columns3,
  Link2, ListTree, Check as CheckIcon,
} from '../icons'
import {
  emptyModel, generateCreate, generateModify, loadModel, uid,
  type Chk, type Col, type FK, type Idx, type TableModel, type Uniq,
} from '../tableModel'
import { kindEngine } from '../dbkinds'

type Kind = 'table' | 'col' | 'uniq' | 'fk' | 'idx' | 'chk'
interface Sel { kind: Kind; uid?: string }

const ON_DELETE = ['', 'CASCADE', 'SET NULL', 'SET DEFAULT', 'RESTRICT', 'NO ACTION']

// DataGrip-style table designer: a tree of columns/keys/foreign keys/indexes/
// checks on the left, a property editor for the selected node on the right, and a
// live SQL preview below. "create" emits one CREATE TABLE; "modify" loads the
// live table and diffs the edits into ALTERs (see tableModel.ts).
export default function TableDesigner({ params, onClose, onApplied }: {
  params: TableDesignerParams; onClose: () => void; onApplied: () => void
}) {
  const app = useApp()
  const { connId, schema, mode, table } = params
  const engine = kindEngine(app.connById(connId)?.kind || 'postgres')
  const [m, setM] = useState<TableModel>(emptyModel)
  const [sel, setSel] = useState<Sel>({ kind: 'table' })
  const [loading, setLoading] = useState(mode === 'modify')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (mode !== 'modify' || !table) return
    api.doc(connId, schema, table)
      .then(d => { setM(loadModel(d)); setLoading(false) })
      .catch(e => { setErr(String(e.message || e)); setLoading(false) })
  }, [mode, connId, schema, table])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const statements = useMemo(() => {
    try { return mode === 'create' ? generateCreate(schema, m, engine) : generateModify(schema, m, engine) }
    catch { return [] }
  }, [mode, schema, m, engine])
  const preview = statements.length ? statements.join(';\n\n') + ';' : '-- nothing to apply'

  const base = (m.name || m.origName || 'table').trim() || 'table'
  const patch = (p: Partial<TableModel>) => setM(s => ({ ...s, ...p }))

  // add / remove / reorder
  const add = (kind: Kind) => {
    const n = (arr: unknown[]) => arr.length + 1
    if (kind === 'col') {
      const c: Col = { uid: uid(), name: `column_${n(m.cols)}`, type: 'text', notNull: false, def: '', pk: false }
      patch({ cols: [...m.cols, c] }); setSel({ kind: 'col', uid: c.uid })
    } else if (kind === 'uniq') {
      const u: Uniq = { uid: uid(), name: `${base}_key_${n(m.uniques)}`, cols: '' }
      patch({ uniques: [...m.uniques, u] }); setSel({ kind: 'uniq', uid: u.uid })
    } else if (kind === 'fk') {
      const f: FK = { uid: uid(), name: `${base}_fk_${n(m.fks)}`, cols: '', refTable: '', refCols: '', onDelete: '' }
      patch({ fks: [...m.fks, f] }); setSel({ kind: 'fk', uid: f.uid })
    } else if (kind === 'idx') {
      const i: Idx = { uid: uid(), name: `${base}_idx_${n(m.indexes)}`, cols: '', unique: false }
      patch({ indexes: [...m.indexes, i] }); setSel({ kind: 'idx', uid: i.uid })
    } else if (kind === 'chk') {
      const c: Chk = { uid: uid(), name: `${base}_check_${n(m.checks)}`, expr: '' }
      patch({ checks: [...m.checks, c] }); setSel({ kind: 'chk', uid: c.uid })
    }
  }
  const removeSel = () => {
    const { kind, uid: u } = sel
    if (!u) return
    if (kind === 'col') patch({ cols: m.cols.filter(c => c.uid !== u) })
    if (kind === 'uniq') patch({ uniques: m.uniques.filter(x => x.uid !== u) })
    if (kind === 'fk') patch({ fks: m.fks.filter(x => x.uid !== u) })
    if (kind === 'idx') patch({ indexes: m.indexes.filter(x => x.uid !== u) })
    if (kind === 'chk') patch({ checks: m.checks.filter(x => x.uid !== u) })
    setSel({ kind: 'table' })
  }
  const moveCol = (dir: -1 | 1) => {
    if (sel.kind !== 'col' || !sel.uid) return
    const i = m.cols.findIndex(c => c.uid === sel.uid)
    const j = i + dir
    if (i < 0 || j < 0 || j >= m.cols.length) return
    const next = m.cols.slice()
    ;[next[i], next[j]] = [next[j], next[i]]
    patch({ cols: next })
  }

  // typed mutators for the selected item
  const updCol = (u: string, p: Partial<Col>) => patch({ cols: m.cols.map(c => c.uid === u ? { ...c, ...p } : c) })
  const updUniq = (u: string, p: Partial<Uniq>) => patch({ uniques: m.uniques.map(x => x.uid === u ? { ...x, ...p } : x) })
  const updFk = (u: string, p: Partial<FK>) => patch({ fks: m.fks.map(x => x.uid === u ? { ...x, ...p } : x) })
  const updIdx = (u: string, p: Partial<Idx>) => patch({ indexes: m.indexes.map(x => x.uid === u ? { ...x, ...p } : x) })
  const updChk = (u: string, p: Partial<Chk>) => patch({ checks: m.checks.map(x => x.uid === u ? { ...x, ...p } : x) })

  const apply = () => {
    setErr('')
    if (mode === 'create' && !m.name.trim()) { setErr('Table name is required'); setSel({ kind: 'table' }); return }
    if (!statements.length) { setErr('No changes to apply'); return }
    setBusy(true)
    api.applyTable(connId, mode === 'create' ? 'sql_ddl_create_table' : 'sql_ddl_modify_table', statements)
      .then(() => { app.notify(mode === 'create' ? 'Table created' : 'Table modified'); onApplied() })
      .catch(e => { setErr(String(e.message || e)); setBusy(false) })
  }

  const title = mode === 'create' ? 'Create table' : `Modify · ${schema}.${table}`
  const canMove = sel.kind === 'col'

  return (
    <div className="modal-overlay">
      <div className="modal modal-wide hud-panel hud-panel-glow">
        <div className="modal-head">
          <span className="hud-heading">{title}</span>
          <button type="button" className="ico-btn" onClick={onClose}><X size={16} /></button>
        </div>

        {loading ? <div className="modal-body"><div className="dim" style={{ padding: '2rem' }}>loading…</div></div> : (
        <div className="modal-body">
          <div className="tdz">
            <div className="tdz-main">
              <div className="tdz-side">
                <div className="tdz-toolbar">
                  <AddMenu onPick={add} />
                  <button type="button" className="tb-ico-btn danger" title="Remove" disabled={!sel.uid} onClick={removeSel}><Minus size={16} /></button>
                  <button type="button" className="tb-ico-btn" title="Move up" disabled={!canMove} onClick={() => moveCol(-1)}><ArrowUp size={16} /></button>
                  <button type="button" className="tb-ico-btn" title="Move down" disabled={!canMove} onClick={() => moveCol(1)}><ArrowDown size={16} /></button>
                </div>
                <div className="tdz-tree">
                  <div className={`tdz-item root${sel.kind === 'table' ? ' on' : ''}`} onClick={() => setSel({ kind: 'table' })}>
                    <Ico name="table" /><span>{m.name || 'table_name'}</span>
                  </div>

                  <Folder label="columns" count={m.cols.length} />
                  {m.cols.length === 0 && <div className="tdz-item empty">no columns</div>}
                  {m.cols.map(c => (
                    <div key={c.uid} className={`tdz-item${sel.uid === c.uid ? ' on' : ''}`} onClick={() => setSel({ kind: 'col', uid: c.uid })}>
                      {c.pk ? <span className="tdz-pk" title="primary key"><KeyRound size={15} /></span> : <Ico name="col" />}
                      <span>{c.name || '(unnamed)'}</span>
                      <span className="it-type">{c.type}</span>
                    </div>
                  ))}

                  <Folder label="keys" count={m.uniques.length + (m.cols.some(c => c.pk) ? 1 : 0)} />
                  {m.cols.some(c => c.pk) && (
                    <div className="tdz-item empty"><span className="tdz-pk"><KeyRound size={14} /></span>&nbsp;PRIMARY KEY ({m.cols.filter(c => c.pk).map(c => c.name).join(', ')})</div>
                  )}
                  {m.uniques.map(u => (
                    <div key={u.uid} className={`tdz-item${sel.uid === u.uid ? ' on' : ''}`} onClick={() => setSel({ kind: 'uniq', uid: u.uid })}>
                      <Ico name="key" /><span>{u.name}</span><span className="it-type">unique</span>
                    </div>
                  ))}

                  <Folder label="foreign keys" count={m.fks.length} />
                  {m.fks.map(f => (
                    <div key={f.uid} className={`tdz-item${sel.uid === f.uid ? ' on' : ''}`} onClick={() => setSel({ kind: 'fk', uid: f.uid })}>
                      <Ico name="fkey" /><span>{f.name}</span><span className="it-type">{f.refTable || 'fk'}</span>
                    </div>
                  ))}

                  <Folder label="indexes" count={m.indexes.length} />
                  {m.indexes.map(i => (
                    <div key={i.uid} className={`tdz-item${sel.uid === i.uid ? ' on' : ''}`} onClick={() => setSel({ kind: 'idx', uid: i.uid })}>
                      <Ico name="idx" /><span>{i.name}</span><span className="it-type">{i.unique ? 'unique' : 'index'}</span>
                    </div>
                  ))}

                  <Folder label="checks" count={m.checks.length} />
                  {m.checks.map(c => (
                    <div key={c.uid} className={`tdz-item${sel.uid === c.uid ? ' on' : ''}`} onClick={() => setSel({ kind: 'chk', uid: c.uid })}>
                      <span className="ico"><CheckIcon size={15} /></span><span>{c.name}</span><span className="it-type">check</span>
                    </div>
                  ))}
                </div>
              </div>

              <div className="tdz-props">
                {sel.kind === 'table' && <TableProps m={m} patch={patch} />}
                {sel.kind === 'col' && pick(m.cols, sel.uid, c => <ColProps key={c.uid} c={c} upd={p => updCol(c.uid, p)} />)}
                {sel.kind === 'uniq' && pick(m.uniques, sel.uid, u => <UniqProps key={u.uid} u={u} upd={p => updUniq(u.uid, p)} />)}
                {sel.kind === 'fk' && pick(m.fks, sel.uid, f => <FkProps key={f.uid} f={f} upd={p => updFk(f.uid, p)} />)}
                {sel.kind === 'idx' && pick(m.indexes, sel.uid, i => <IdxProps key={i.uid} i={i} upd={p => updIdx(i.uid, p)} />)}
                {sel.kind === 'chk' && pick(m.checks, sel.uid, c => <ChkProps key={c.uid} c={c} upd={p => updChk(c.uid, p)} />)}
              </div>
            </div>

            <div className="tdz-preview">
              <div className="tdz-preview-head hud-label"><span>Preview</span><span className="tb-grow" /><span className="dim">{statements.length} statement{statements.length === 1 ? '' : 's'}</span></div>
              {engine === 'mysql' && statements.length > 1 && (
                <div className="hint dim" style={{ padding: '.2rem .6rem' }}>
                  MySQL DDL is not transactional: if a statement fails partway, earlier ones stay applied (no rollback).
                </div>
              )}
              <pre>{preview}</pre>
            </div>
          </div>
        </div>
        )}

        <div className="modal-foot" style={{ margin: 0, padding: '.7rem .9rem' }}>
          {err && <span className="bad code" style={{ fontSize: '.78rem' }}>{err}</span>}
          <span className="tb-grow" />
          <button type="button" className="hud-btn-accent" onClick={onClose}>Cancel</button>
          <button type="button" className="hud-btn-cta" disabled={busy || loading} onClick={apply}>{busy ? '…' : 'OK'}</button>
        </div>
      </div>
    </div>
  )
}

function pick<T extends { uid: string }>(arr: T[], u: string | undefined, render: (x: T) => React.ReactNode) {
  const x = arr.find(i => i.uid === u)
  return x ? render(x) : null
}

function Folder({ label, count }: { label: string; count: number }) {
  return <div className="tdz-folder"><Ico name="folder" /><span>{label}</span><span className="count">{count}</span></div>
}

// add dropdown
function AddMenu({ onPick }: { onPick: (k: Kind) => void }) {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (!open) return
    const close = () => setOpen(false)
    document.addEventListener('click', close)
    return () => document.removeEventListener('click', close)
  }, [open])
  const item = (k: Kind, label: string, Glyph: LucideIcon) => (
    <button type="button" className="menu-item" onClick={() => { setOpen(false); onPick(k) }}>
      <span className="mi-ico"><Glyph size={15} /></span> {label}
    </button>
  )
  return (
    <div className="tdz-addmenu" onClick={e => e.stopPropagation()}>
      <button type="button" className="tb-ico-btn" title="Add" onClick={() => setOpen(o => !o)}><Plus size={16} /></button>
      {open && (
        <div className="menu">
          {item('col', 'Column', Columns3)}
          {item('uniq', 'Unique key', KeyRound)}
          {item('fk', 'Foreign key', Link2)}
          {item('idx', 'Index', ListTree)}
          {item('chk', 'Check', CheckIcon)}
        </div>
      )}
    </div>
  )
}

// property panels
function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="mrow"><label className="hud-label">{label}</label>{children}</div>
}
function Check({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return <label className="check"><input type="checkbox" checked={checked} onChange={e => onChange(e.target.checked)} /> <span className="hud-label">{label}</span></label>
}

function TableProps({ m, patch }: { m: TableModel; patch: (p: Partial<TableModel>) => void }) {
  return <>
    <Row label="Name"><input className="hud-input" autoFocus value={m.name} onChange={e => patch({ name: e.target.value })} placeholder="table_name" /></Row>
    <Row label="Comment"><input className="hud-input" value={m.comment} onChange={e => patch({ comment: e.target.value })} /></Row>
    <div className="hint dim">Use the + toolbar to add columns, keys, foreign keys, indexes and checks. Each node opens here for editing.</div>
  </>
}
function ColProps({ c, upd }: { c: Col; upd: (p: Partial<Col>) => void }) {
  return <>
    <Row label="Name"><input className="hud-input" autoFocus value={c.name} onChange={e => upd({ name: e.target.value })} /></Row>
    <Row label="Type"><input className="hud-input code" value={c.type} onChange={e => upd({ type: e.target.value })} placeholder="varchar(255) · integer · timestamptz · uuid · jsonb" /></Row>
    <div className="mrow2">
      <Check checked={c.notNull} onChange={v => upd({ notNull: v })} label="Not null" />
      <Check checked={c.pk} onChange={v => upd({ pk: v, notNull: v ? true : c.notNull })} label="Primary key" />
    </div>
    <Row label="Default (SQL expression)"><input className="hud-input code" value={c.def} onChange={e => upd({ def: e.target.value })} placeholder="0 · now() · gen_random_uuid()" /></Row>
  </>
}
function UniqProps({ u, upd }: { u: Uniq; upd: (p: Partial<Uniq>) => void }) {
  return <>
    <Row label="Constraint name"><input className="hud-input" autoFocus value={u.name} onChange={e => upd({ name: e.target.value })} /></Row>
    <Row label="Columns (comma-separated)"><input className="hud-input code" value={u.cols} onChange={e => upd({ cols: e.target.value })} placeholder="email · tenant_id, slug" /></Row>
  </>
}
function FkProps({ f, upd }: { f: FK; upd: (p: Partial<FK>) => void }) {
  return <>
    <Row label="Constraint name"><input className="hud-input" autoFocus value={f.name} onChange={e => upd({ name: e.target.value })} /></Row>
    <Row label="Columns (comma-separated)"><input className="hud-input code" value={f.cols} onChange={e => upd({ cols: e.target.value })} placeholder="user_id" /></Row>
    <Row label="References table"><input className="hud-input code" value={f.refTable} onChange={e => upd({ refTable: e.target.value })} placeholder="public.users" /></Row>
    <Row label="Referenced columns"><input className="hud-input code" value={f.refCols} onChange={e => upd({ refCols: e.target.value })} placeholder="id" /></Row>
    <Row label="On delete">
      <select className="hud-input" value={f.onDelete} onChange={e => upd({ onDelete: e.target.value })}>
        {ON_DELETE.map(o => <option key={o} value={o}>{o || 'NO ACTION (default)'}</option>)}
      </select>
    </Row>
  </>
}
function IdxProps({ i, upd }: { i: Idx; upd: (p: Partial<Idx>) => void }) {
  return <>
    <Row label="Index name"><input className="hud-input" autoFocus value={i.name} onChange={e => upd({ name: e.target.value })} /></Row>
    <Row label="Columns / expression"><input className="hud-input code" value={i.cols} onChange={e => upd({ cols: e.target.value })} placeholder="email · lower(email) · created_at desc" /></Row>
    <Check checked={i.unique} onChange={v => upd({ unique: v })} label="Unique" />
  </>
}
function ChkProps({ c, upd }: { c: Chk; upd: (p: Partial<Chk>) => void }) {
  return <>
    <Row label="Constraint name"><input className="hud-input" autoFocus value={c.name} onChange={e => upd({ name: e.target.value })} /></Row>
    <Row label="Expression"><input className="hud-input code" value={c.expr} onChange={e => upd({ expr: e.target.value })} placeholder="amount >= 0" /></Row>
  </>
}
