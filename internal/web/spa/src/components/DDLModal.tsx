import { useEffect, useState } from 'react'
import { api } from '../api'
import { useApp, type DDLParams } from '../appctx'
import { X } from '../icons'

const TITLES: Record<string, string> = {
  'add-column': 'Add column', 'modify-column': 'Modify column', 'rename-table': 'Rename table',
  'new-schema': 'New schema', 'new-table': 'New table', 'new-index': 'New index',
  'create-user': 'New user / role', 'alter-schema': 'Alter schema', 'alter-user': 'Edit role / user',
}

interface FormState {
  name: string; type: string; nullable: boolean; default: string; columns: string; unique: boolean
  password: string; login: boolean; createdb: boolean; createrole: boolean; superuser: boolean
  owner: string
}

// Parameter modal for form-backed DDL (add/modify column, rename, new schema/
// table/index). Mirrors the "ddlForm" partial; on apply it POSTs the fields as
// JSON, and on success the caller refreshes the tree.
export default function DDLModal({ params, onClose, onApplied }: {
  params: DDLParams; onClose: () => void; onApplied: () => void
}) {
  const app = useApp()
  const { connId, kind, schema, table, column } = params
  const [f, setF] = useState<FormState>({ name: '', type: '', nullable: true, default: '', columns: '', unique: false, password: '', login: true, createdb: false, createrole: false, superuser: false, owner: '' })
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

  // Prefill the edit forms: schema rename starts from the current name; role
  // edit starts from the role's live attributes (carried in the payload).
  useEffect(() => {
    if (kind === 'alter-schema') setF(s => ({ ...s, name: schema }))
    if (kind === 'alter-user' && params.role) {
      const r = params.role
      setF(s => ({ ...s, name: r.Name, password: '', login: r.CanLogin, createdb: r.CreateDB, createrole: r.CreateRole, superuser: r.Super }))
    }
  }, [kind, schema, params.role])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const submit = (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    // alter-schema/alter-user compile to multi-statement edits with their own
    // endpoints; everything else goes through the generic runForm.
    let p: Promise<unknown>
    if (kind === 'alter-schema') {
      p = api.alterSchema(connId, schema, f.name, f.owner)
    } else if (kind === 'alter-user') {
      p = api.alterRole(connId, {
        name: params.role?.Name || '', newName: f.name, password: f.password,
        login: f.login, createdb: f.createdb, createrole: f.createrole, superuser: f.superuser,
      })
    } else {
      p = api.runForm(connId, {
        kind, schema, table, column,
        name: f.name, type: f.type, default: f.default, columns: f.columns,
        nullable: f.nullable, unique: f.unique,
        password: f.password, login: f.login, createdb: f.createdb, createrole: f.createrole, superuser: f.superuser,
        owner: f.owner,
      })
    }
    p.then(() => { app.notify('Applied'); onApplied() }).catch(x => setErr(String(x.message || x)))
  }

  return (
    <div className="modal-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal hud-panel hud-panel-glow">
        <div className="modal-head">
          <span className="hud-heading">{TITLES[kind] || 'DDL'}</span>
          <button type="button" className="ico-btn" onClick={onClose}><X size={16} /></button>
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

          {kind === 'new-schema' && <>
            <Row label="Schema name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} placeholder="reporting" /></Row>
            <Row label="Owner (optional)"><input className="hud-input" value={f.owner} onChange={e => set('owner', e.target.value)} placeholder="defaults to current role" /></Row>
          </>}

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

          {kind === 'create-user' && <>
            <Row label="Role name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} placeholder="reporting" /></Row>
            <Row label="Password (blank = none)"><input className="hud-input" type="password" autoComplete="new-password" value={f.password} onChange={e => set('password', e.target.value)} /></Row>
            <Check checked={f.login} onChange={v => set('login', v)} label="Can log in (user)" />
            <Check checked={f.createdb} onChange={v => set('createdb', v)} label="Create databases" />
            <Check checked={f.createrole} onChange={v => set('createrole', v)} label="Create roles" />
            <Check checked={f.superuser} onChange={v => set('superuser', v)} label="Superuser" />
          </>}

          {kind === 'alter-schema' && <>
            <Row label="Schema"><input className="hud-input" value={schema} disabled /></Row>
            <Row label="New name"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} /></Row>
            <Row label="Owner (blank = unchanged)"><input className="hud-input" value={f.owner} onChange={e => set('owner', e.target.value)} placeholder="role to own this schema" /></Row>
          </>}

          {kind === 'alter-user' && <>
            <Row label="Role"><input className="hud-input" value={params.role?.Name || ''} disabled /></Row>
            <Row label="Rename to"><input className="hud-input" required value={f.name} onChange={e => set('name', e.target.value)} /></Row>
            <Row label="New password (blank = unchanged)"><input className="hud-input" type="password" autoComplete="new-password" value={f.password} onChange={e => set('password', e.target.value)} /></Row>
            <Check checked={f.login} onChange={v => set('login', v)} label="Can log in (user)" />
            <Check checked={f.createdb} onChange={v => set('createdb', v)} label="Create databases" />
            <Check checked={f.createrole} onChange={v => set('createrole', v)} label="Create roles" />
            <Check checked={f.superuser} onChange={v => set('superuser', v)} label="Superuser" />
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
