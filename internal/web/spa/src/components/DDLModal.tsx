import { useEffect, useState } from 'react'
import { api } from '../api'
import { useApp, type DDLParams } from '../appctx'

const TITLES: Record<string, string> = {
  'add-column': 'Add column', 'modify-column': 'Modify column', 'rename-table': 'Rename table',
  'new-schema': 'New schema', 'new-table': 'New table', 'new-index': 'New index',
}

interface FormState {
  name: string; type: string; nullable: boolean; default: string; columns: string; unique: boolean
}

// Parameter modal for form-backed DDL (add/modify column, rename, new schema/
// table/index). Mirrors the "ddlForm" partial; on apply it POSTs the fields as
// JSON, and on success the caller refreshes the tree.
export default function DDLModal({ params, onClose, onApplied }: {
  params: DDLParams; onClose: () => void; onApplied: () => void
}) {
  const app = useApp()
  const { connId, kind, schema, table, column } = params
  const [f, setF] = useState<FormState>({ name: '', type: '', nullable: true, default: '', columns: '', unique: false })
  const [err, setErr] = useState('')
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF(p => ({ ...p, [k]: v }))

  // Prefill the modify-column form from the live column definition.
  useEffect(() => {
    if (kind === 'modify-column') {
      api.ddlPrefill(connId, kind, schema, table, column || '').then(p =>
        setF(s => ({ ...s, name: p.name || '', type: p.type || '', nullable: p.nullable, default: p.default || '' })))
        .catch(() => {})
    }
  }, [connId, kind, schema, table, column])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    api.runForm(connId, {
      kind, schema, table, column,
      name: f.name, type: f.type, default: f.default, columns: f.columns,
      nullable: f.nullable, unique: f.unique,
    }).then(() => { app.notify('Applied'); onApplied() }).catch(x => setErr(String(x.message || x)))
  }

  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal hud-panel hud-panel-glow">
        <div className="modal-head">
          <span className="hud-heading">{TITLES[kind] || 'DDL'}</span>
          <button type="button" className="ico-btn" onClick={onClose}>✕</button>
        </div>
        <form className="modal-body" onSubmit={submit}>
          {err && <div className="alert error code">{err}</div>}

          {kind === 'add-column' && <>
            <Row label="Column name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} /></Row>
            <Row label="Type"><input className="hud-input code" required value={f.type} onChange={e => set('type', e.target.value)} placeholder="varchar(255) · integer · timestamptz · jsonb" /></Row>
            <Check checked={f.nullable} onChange={v => set('nullable', v)} label="Nullable" />
            <Row label="Default (SQL expression)"><input className="hud-input code" value={f.default} onChange={e => set('default', e.target.value)} placeholder="0 · now() · ''::text" /></Row>
          </>}

          {kind === 'modify-column' && <>
            <Row label="Column"><input className="hud-input" value={column || ''} disabled /></Row>
            <Row label="Type"><input className="hud-input code" required value={f.type} onChange={e => set('type', e.target.value)} /></Row>
            <Check checked={f.nullable} onChange={v => set('nullable', v)} label="Nullable" />
            <Row label="Default (blank = none)"><input className="hud-input code" value={f.default} onChange={e => set('default', e.target.value)} /></Row>
          </>}

          {kind === 'rename-table' && <>
            <Row label="Current"><input className="hud-input" value={`${schema}.${table}`} disabled /></Row>
            <Row label="New name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} /></Row>
          </>}

          {kind === 'new-schema' &&
            <Row label="Schema name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} /></Row>}

          {kind === 'new-table' && <>
            <Row label="Schema"><input className="hud-input" value={schema} disabled /></Row>
            <Row label="Table name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} /></Row>
            <Row label="Columns (SQL column definitions)">
              <textarea className="hud-input code" rows={6} value={f.columns} onChange={e => set('columns', e.target.value)}
                placeholder={'id bigserial primary key,\nname text not null,\ncreated_at timestamptz default now()'} /></Row>
          </>}

          {kind === 'new-index' && <>
            <Row label="On"><input className="hud-input" value={`${schema}.${table}`} disabled /></Row>
            <Row label="Index name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} /></Row>
            <Row label="Columns"><input className="hud-input code" required value={f.columns} onChange={e => set('columns', e.target.value)} placeholder="email · lower(email) · created_at desc" /></Row>
            <Check checked={f.unique} onChange={v => set('unique', v)} label="Unique" />
          </>}

          <div className="modal-foot">
            <span className="hud-label dim">{schema}{table ? `.${table}` : ''}</span>
            <span className="tb-grow" />
            <button type="button" className="hud-btn-accent" onClick={onClose}>Cancel</button>
            <button type="submit" className="hud-btn-cta">Apply</button>
          </div>
        </form>
      </div>
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="mrow"><label className="hud-label">{label}</label>{children}</div>
}
function Check({ checked, onChange, label }: { checked: boolean; onChange: (v: boolean) => void; label: string }) {
  return <label className="check"><input type="checkbox" checked={checked} onChange={e => onChange(e.target.checked)} /> <span className="hud-label">{label}</span></label>
}
