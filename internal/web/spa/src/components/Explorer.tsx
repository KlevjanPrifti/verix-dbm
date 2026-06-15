import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import { useApp, type NodePayload, type TabView } from '../appctx'
import type { Column, Connection, Index, Key, Role, Schema } from '../types'
import { Ico, nameColor, Plus, Terminal, Trash2, MoreHorizontal } from '../icons'
import { DB_KINDS } from '../dbkinds'

// Database Explorer: a lazy-loaded tree of connections → schemas → tables →
// columns/keys/indexes. Disclosure uses native <details>; each row wires the
// shared context menu (right-click + ⋯ kebab).

export default function Explorer({ open }: { open: boolean }) {
  const app = useApp()
  return (
    <aside className={`explorer hud-panel${open ? ' open' : ''}`}>
      <div className="explorer-head">
        <span className="hud-label">Database Explorer</span>
        {app.caps.admin && <NewSourceMenu />}
      </div>
      <div className="explorer-tree">
        {app.conns.length === 0
          ? <div className="tree-empty dim">No connections.{app.caps.admin ? ' Click + to add one.' : ''}</div>
          : app.conns.map(c => <ConnNode key={c.id} conn={c} />)}
      </div>
    </aside>
  )
}

function NewSourceMenu() {
  const app = useApp()
  const [open, setOpen] = useState(false)
  useEffect(() => {
    if (!open) return
    const close = () => setOpen(false)
    document.addEventListener('click', close)
    return () => document.removeEventListener('click', close)
  }, [open])
  return (
    <div className="menu-wrap" onClick={e => e.stopPropagation()}>
      <button className="ico-btn" title="New data source" onClick={() => setOpen(o => !o)}><Plus size={16} /></button>
      {open && (
        <div className="menu">
          {DB_KINDS.map(k => (
            <button key={k.id} type="button" className="menu-item"
              onClick={() => { setOpen(false); app.openConnModal(k.id) }}>
              <Ico name={k.id} /> New {k.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function ConnNode({ conn }: { conn: Connection }) {
  const app = useApp()
  const [data, setData] = useState<{ kind: string; schemas?: Schema[] | null; error?: string } | null>(null)
  const [openOnce, setOpenOnce] = useState(false)
  const token = app.refreshToken(conn.id)
  const ref = useRef<HTMLDetailsElement>(null)

  // (Re)load when first expanded, and whenever this conn is asked to refresh.
  useEffect(() => {
    if (!openOnce) return
    api.explorer(conn.id).then(setData).catch(e => setData({ kind: conn.kind, error: String(e.message || e) }))
  }, [openOnce, token, conn.id, conn.kind])

  // When the active tab lives under this connection, expand it so the user can
  // see where they are. A conn-level tab (console/redis) highlights the row itself.
  const av = app.activeView
  const hasActive = av?.connId === conn.id
  const rowActive = hasActive && (av.type === 'console' || av.type === 'redis')
  useEffect(() => {
    if (hasActive && ref.current) { ref.current.open = true; setOpenOnce(true) }
  }, [hasActive, av])

  const payload: NodePayload = { type: 'conn', connId: conn.id, name: conn.name, kind: conn.kind }
  const openConsole = () => app.openTab({
    key: `console:${conn.id}`, title: `console [${conn.name}]`, icon: 'console',
    view: { type: 'console', connId: conn.id },
  })

  return (
    <details ref={ref} className="tree-node conn-node" onToggle={e => { if ((e.target as HTMLDetailsElement).open) setOpenOnce(true) }}>
      <summary
        className={`tree-row conn-row${rowActive ? ' active' : ''}`}
        onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}
      >
        <Ico name={conn.kind} color={nameColor(conn.name)} />
        <span className="tree-name">{conn.name}<span className="conn-host dim"> · {conn.host}</span></span>
        <span className="badge">{conn.kind}</span>
        {conn.readOnly && <span className="ro-dot" title="read-only" />}
        <span className="row-acts">
          <button type="button" className="row-act" title="open console"
            onClick={e => { e.stopPropagation(); e.preventDefault(); openConsole() }}><Terminal size={14} /></button>
          {app.caps.admin && (
            <button type="button" className="row-act danger" title="delete"
              onClick={async e => {
                e.stopPropagation(); e.preventDefault()
                const ok = await app.confirm({ title: 'Delete connection', body: `Delete connection “${conn.name}”?`, buttons: [{ label: 'Delete', value: 'ok', variant: 'danger' }] })
                if (ok)
                  api.deleteConnection(conn.id).then(app.reloadConns).catch(err => app.notify(String(err.message || err), 'error'))
              }}><Trash2 size={14} /></button>
          )}
        </span>
        <Kebab payload={payload} />
      </summary>
      <div className="node-children">
        {!data ? <span className="dim loading">loading…</span>
          : data.error ? <div className="tree-err code">{data.error}</div>
          : data.kind === 'redis'
            ? <div className="tree-row leaf" onClick={openConsole}><Ico name="keyspace" /><span className="tree-name">keyspace</span></div>
            : <><SchemaList connId={conn.id} schemas={data.schemas} />{app.caps.admin && <RolesNode connId={conn.id} />}</>}
      </div>
    </details>
  )
}

function SchemaList({ connId, schemas }: { connId: number; schemas?: Schema[] | null }) {
  if (!schemas || schemas.length === 0) return <div className="tree-empty dim">no user schemas</div>
  return <>{schemas.map(s => <SchemaNode key={s.Name} connId={connId} schema={s} defaultOpen={schemas.length === 1} />)}</>
}

function SchemaNode({ connId, schema, defaultOpen }: { connId: number; schema: Schema; defaultOpen: boolean }) {
  const app = useApp()
  const payload: NodePayload = { type: 'schema', connId, schema: schema.Name, name: schema.Name }
  const tables = schema.Tables || []
  const ref = useRef<HTMLDetailsElement>(null)
  // Reveal the schema when the active tab's table lives inside it.
  const av = app.activeView
  const hasActive = !!av && 'table' in av && av.connId === connId && av.schema === schema.Name
  useEffect(() => { if (hasActive && ref.current) ref.current.open = true }, [hasActive, av])
  return (
    <details ref={ref} className="tree-node" open={defaultOpen}>
      <summary className="tree-row schema-row"
        onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}>
        <Ico name="schema" /><span className="tree-name">{schema.Name}</span>
        <span className="count">{tables.length}</span>
        <Kebab payload={payload} />
      </summary>
      <div className="node-children">
        {tables.map(t => <TableNode key={t.Name} connId={connId} schema={schema.Name} table={t.Name} kind={t.Kind} />)}
      </div>
    </details>
  )
}

function TableNode({ connId, schema, table, kind }: { connId: number; schema: string; table: string; kind: string }) {
  const app = useApp()
  const payload: NodePayload = { type: 'table', connId, schema, table, name: table }
  const active = tableActive(app.activeView, connId, schema, table)
  const openGrid = () => app.openTab({
    key: `grid:${connId}:${schema}.${table}`, title: `${schema}.${table}`, icon: 'grid',
    view: { type: 'grid', connId, schema, table },
  })
  return (
    <details className="tree-node">
      <summary className={`tree-row table-row${active ? ' active' : ''}`}
        onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}>
        <Ico name={kind} />
        <span className="tree-name table-name" title="click name to open"
          onClick={e => { e.preventDefault(); openGrid() }}>{table}</span>
        <Kebab payload={payload} />
      </summary>
      <div className="node-children">
        <LeafFolder label="columns" load={() => api.columns(connId, schema, table).then(r => r.columns)}
          render={(cols: Column[]) => cols.map(c => <ColumnLeaf key={c.name} connId={connId} schema={schema} table={table} col={c} />)} />
        <LeafFolder label="keys" load={() => api.keys(connId, schema, table).then(r => r.keys || [])}
          render={(keys: Key[]) => keys.map(k => <KeyLeaf key={k.Name} connId={connId} schema={schema} table={table} k={k} />)} />
        <LeafFolder label="indexes" load={() => api.indexes(connId, schema, table).then(r => r.indexes || [])}
          render={(ix: Index[]) => ix.map(i => <IndexLeaf key={i.Name} connId={connId} schema={schema} table={table} ix={i} />)} />
      </div>
    </details>
  )
}

function LeafFolder<T>({ label, load, render }: { label: string; load: () => Promise<T[]>; render: (items: T[]) => React.ReactNode }) {
  const [items, setItems] = useState<T[] | null>(null)
  const [err, setErr] = useState('')
  return (
    <details className="tree-node" onToggle={e => {
      if ((e.target as HTMLDetailsElement).open && items === null && !err)
        load().then(setItems).catch(x => setErr(String(x.message || x)))
    }}>
      <summary className="tree-row"><Ico name="folder" /><span className="tree-name">{label}</span></summary>
      <div className="node-children">
        {err ? <div className="tree-err code">{err}</div>
          : items === null ? <span className="dim loading">…</span>
          : items.length === 0 ? <div className="tree-empty dim">none</div>
          : render(items)}
      </div>
    </details>
  )
}

function ColumnLeaf({ connId, schema, table, col }: { connId: number; schema: string; table: string; col: Column }) {
  const app = useApp()
  const payload: NodePayload = { type: 'col', connId, schema, table, name: col.name }
  return (
    <div className="tree-row leaf col-leaf" title={`${col.name} · ${col.typeText}${col.notNull ? ' · not null' : ''}`}
      onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}>
      <Ico name={col.cat} />
      <span className="tree-name">{col.name}</span>
      <span className="col-type dim">{col.typeText}</span>
      <Kebab payload={payload} />
    </div>
  )
}

function KeyLeaf({ connId, schema, table, k }: { connId: number; schema: string; table: string; k: Key }) {
  const app = useApp()
  const payload: NodePayload = { type: 'key', connId, schema, table, name: k.Name, def: k.Def }
  return (
    <div className="tree-row leaf" title={k.Def}
      onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}>
      <Ico name={k.Type === 'foreign' ? 'fkey' : 'key'} className={k.Type === 'primary' ? 'pk' : undefined} />
      <span className="tree-name">{k.Name}</span>
      <span className="col-type dim">{k.Cols ? `(${k.Cols}) ` : ''}{k.Type}</span>
      <Kebab payload={payload} />
    </div>
  )
}

function IndexLeaf({ connId, schema, table, ix }: { connId: number; schema: string; table: string; ix: Index }) {
  const app = useApp()
  const payload: NodePayload = { type: 'index', connId, schema, table, name: ix.Name, def: ix.Def }
  return (
    <div className="tree-row leaf" title={ix.Def}
      onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}>
      <Ico name="idx" className={ix.Primary ? 'pk' : undefined} />
      <span className="tree-name">{ix.Name}</span>
      <span className="col-type dim">{ix.Cols ? `(${ix.Cols})` : ''}{ix.Unique ? ' UNIQUE' : ''}</span>
      <Kebab payload={payload} />
    </div>
  )
}

// RolesNode is the admin-only "roles" folder under a Postgres connection: a
// lazy-loaded list of cluster roles/users. It reloads on the connection's
// refresh token, so create/edit/drop from the menu shows up immediately.
function RolesNode({ connId }: { connId: number }) {
  const app = useApp()
  const [roles, setRoles] = useState<Role[] | null>(null)
  const [err, setErr] = useState('')
  const [openOnce, setOpenOnce] = useState(false)
  const token = app.refreshToken(connId)
  useEffect(() => {
    if (!openOnce) return
    setErr('')
    api.roles(connId).then(r => setRoles(r.roles || [])).catch(x => { setErr(String(x.message || x)); setRoles([]) })
  }, [openOnce, token, connId])
  const payload: NodePayload = { type: 'roles', connId, name: 'roles' }
  return (
    <details className="tree-node" onToggle={e => { if ((e.target as HTMLDetailsElement).open) setOpenOnce(true) }}>
      <summary className="tree-row"
        onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}>
        <Ico name="roles" /><span className="tree-name">roles</span>
        {roles && <span className="count">{roles.length}</span>}
        <Kebab payload={payload} />
      </summary>
      <div className="node-children">
        {err ? <div className="tree-err code">{err}</div>
          : roles === null ? <span className="dim loading">…</span>
          : roles.length === 0 ? <div className="tree-empty dim">none</div>
          : roles.map(r => <RoleLeaf key={`${r.Name}@${r.Host}`} connId={connId} role={r} />)}
      </div>
    </details>
  )
}

function RoleLeaf({ connId, role }: { connId: number; role: Role }) {
  const app = useApp()
  // MySQL accounts are user@host; show the host so two same-named users differ.
  const display = role.Host ? `${role.Name}@${role.Host}` : role.Name
  const payload: NodePayload = { type: 'role', connId, name: role.Name, role }
  const attrs = roleAttrText(role)
  return (
    <div className="tree-row leaf" title={attrs ? `${display} · ${attrs}` : display}
      onContextMenu={e => { e.preventDefault(); app.openCtx(e.clientX, e.clientY, payload) }}>
      <Ico name="role" className={role.Super ? 'pk' : undefined} />
      <span className="tree-name">{display}</span>
      {attrs && <span className="col-type dim">{attrs}</span>}
      <Kebab payload={payload} />
    </div>
  )
}

// roleAttrText summarises a role's notable privileges for the tree row.
function roleAttrText(r: Role): string {
  const t: string[] = []
  if (r.Super) t.push('super')
  if (!r.CanLogin) t.push('nologin')
  if (r.CreateDB) t.push('createdb')
  if (r.CreateRole) t.push('createrole')
  return t.join(' · ')
}

// tableActive reports whether the active tab (grid/doc/usages) targets this table.
function tableActive(av: TabView | null, connId: number, schema: string, table: string): boolean {
  return !!av && 'table' in av && av.connId === connId && av.schema === schema && av.table === table
}

// Kebab opens the same context menu as right-click the touch-friendly path.
function Kebab({ payload }: { payload: NodePayload }) {
  const app = useApp()
  return (
    <button type="button" className="row-kebab" title="actions"
      onClick={e => {
        e.stopPropagation(); e.preventDefault()
        const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
        app.openCtx(r.left, r.bottom, payload)
      }}><MoreHorizontal size={16} /></button>
  )
}
