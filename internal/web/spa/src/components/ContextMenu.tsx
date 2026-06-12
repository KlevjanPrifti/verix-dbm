import { useEffect, useRef, useState } from 'react'
import { api } from '../api'
import { useApp, type DDLKind, type NodePayload } from '../appctx'
import {
  type LucideIcon, Terminal, Plus, Box, RotateCw, Copy, Settings, Trash2, Table2,
  Code, Download, FileCode, Info, Search, Pencil, ListTree, Eraser, ChevronRight,
  UserPlus,
} from '../icons'

interface MenuItem {
  label?: string
  Icon?: LucideIcon
  key?: string
  danger?: boolean
  head?: string
  sep?: boolean
  run?: () => void
  children?: MenuItem[]
}

const SEP: MenuItem = { sep: true }
const MW = 240, MH = 320

// ContextMenu builds a DataGrip-style menu model from a tree node and the user's
// caps, then renders it with a single level of fly-out submenus. Ported from the
// old Alpine buildMenu(); every item carries a Lucide icon (see icons.tsx).
export default function ContextMenu({ x, y, payload, onClose }: {
  x: number; y: number; payload: NodePayload; onClose: () => void
}) {
  const app = useApp()
  const [openSub, setOpenSub] = useState<number | null>(null)
  const id = payload.connId
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    // Drop any page text selection so the OS selection handles/indicators don't
    // float on top of the menu (most visible as a bottom sheet on phones).
    window.getSelection()?.removeAllRanges()
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    // Close on any pointer/right-click outside the menu (capture phase, so it
    // works regardless of stacking context or stopped propagation).
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

  const close = onClose
  const qq = (s: string) => '"' + s.replace(/"/g, '""') + '"'
  const tab = {
    console: () => app.openTab({
      key: payload.schema ? `console:${id}:${payload.schema}` : `console:${id}`,
      title: `console [${payload.name}]`, icon: 'console',
      view: { type: 'console', connId: id, schema: payload.schema },
    }),
    // Open a query console seeded with a runnable SELECT for this table.
    tableQuery: () => app.openTab({
      key: `query:${id}:${payload.schema}.${payload.table}`, title: `query · ${payload.table}`, icon: 'console',
      view: { type: 'console', connId: id, schema: payload.schema, sql: `SELECT *\nFROM ${qq(payload.schema!)}.${qq(payload.table!)}\nLIMIT 100;` },
    }),
    grid: () => app.openTab({ key: `grid:${id}:${payload.schema}.${payload.table}`, title: `${payload.schema}.${payload.table}`, icon: 'grid', view: { type: 'grid', connId: id, schema: payload.schema!, table: payload.table! } }),
    doc: () => app.openTab({ key: `doc:${id}:${payload.schema}.${payload.table}`, title: `doc [${payload.table}]`, icon: 'grid', view: { type: 'doc', connId: id, schema: payload.schema!, table: payload.table! } }),
    usages: () => app.openTab({ key: `usages:${id}:${payload.schema}.${payload.table}`, title: `usages [${payload.table}]`, icon: 'grid', view: { type: 'usages', connId: id, schema: payload.schema!, table: payload.table! } }),
  }
  const generate = (kind: string) =>
    api.generate(id, kind, payload.schema!, payload.table!).then(r => app.copy(r.sql)).catch(e => app.notify(String(e.message || e), 'error'))
  const form = (kind: DDLKind, column?: string) => app.openDDL({ connId: id, kind, schema: payload.schema || '', table: payload.table || '', column, role: payload.role })
  const confirmRun = async (msg: string, run: () => Promise<unknown>, refresh = false) => {
    const ok = await app.confirm({ title: 'Please confirm', body: msg, buttons: [{ label: 'Confirm', value: 'ok', variant: 'danger' }] })
    if (!ok) return
    try {
      await run()
      if (refresh) app.refreshConn(id)
    } catch (e) {
      app.notify(String((e as Error).message || e), 'error')
    }
  }

  const items = buildMenu()
  function buildMenu(): MenuItem[] {
    const w = app.caps.write, a = app.caps.admin
    const c = payload
    const m: MenuItem[] = []
    if (c.type === 'conn') {
      m.push({ head: c.name })
      m.push({ label: 'Query console', Icon: Terminal, key: 'Ctrl+⏎', run: tab.console })
      if (w) {
        const newKids: MenuItem[] = [
          { label: 'Query console', Icon: Terminal, run: tab.console },
          { label: 'Schema…', Icon: Box, run: () => form('new-schema') },
        ]
        if (a) newKids.push({ label: 'User / role…', Icon: UserPlus, run: () => form('create-user') })
        m.push({ label: 'New…', Icon: Plus, children: newKids })
      }
      m.push({ label: 'Refresh', Icon: RotateCw, key: 'Ctrl+F5', run: () => { close(); app.refreshConn(id) } })
      m.push(SEP)
      m.push({ label: 'Copy name', Icon: Copy, run: () => app.copy(c.name) })
      if (a) {
        m.push({ label: 'Properties', Icon: Settings, key: 'F4', run: () => app.openEditModal(id) })
        m.push({ label: 'Duplicate…', Icon: Copy, run: () => app.openEditModal(id) })
        m.push(SEP)
        m.push({ label: 'Remove data source', Icon: Trash2, key: 'Del', danger: true, run: () => confirmRun(`Remove data source “${c.name}”?`, () => api.deleteConnection(id).then(app.reloadConns)) })
      }
    } else if (c.type === 'schema') {
      m.push({ head: c.schema! })
      m.push({ label: 'Query console', Icon: Terminal, run: tab.console })
      if (w) m.push({ label: 'New…', Icon: Plus, children: [
        { label: 'Table…', Icon: Table2, run: () => app.openTableDesigner({ connId: id, schema: c.schema!, mode: 'create' }) },
        { label: 'Table (raw SQL)…', Icon: Code, run: () => form('new-table') },
      ] })
      if (w) m.push({ label: 'Rename / owner…', Icon: Pencil, run: () => form('alter-schema') })
      m.push({ label: 'Refresh', Icon: RotateCw, key: 'Ctrl+F5', run: () => { close(); app.refreshConn(id) } })
      m.push(SEP)
      m.push({ label: 'Copy name', Icon: Copy, run: () => app.copy(c.schema!) })
      if (a) {
        m.push(SEP)
        m.push({ label: 'Drop schema…', Icon: Trash2, danger: true, run: () => dropSchema(c.schema!) })
      }
    } else if (c.type === 'roles') {
      m.push({ head: 'roles' })
      if (a) m.push({ label: 'New role / user…', Icon: UserPlus, run: () => form('create-user') })
      m.push({ label: 'Refresh', Icon: RotateCw, run: () => { close(); app.refreshConn(id) } })
    } else if (c.type === 'role') {
      m.push({ head: c.name })
      m.push({ label: 'Copy name', Icon: Copy, run: () => app.copy(c.name) })
      if (a) {
        m.push(SEP)
        m.push({ label: 'Edit role…', Icon: Pencil, run: () => form('alter-user') })
        m.push({ label: 'Drop role…', Icon: Trash2, danger: true, run: () => confirmRun(`Drop role “${c.name}”? This cannot be undone.`, () => api.dropRole(id, c.name), true) })
      }
    } else if (c.type === 'table') {
      m.push({ head: `${c.schema}.${c.table}` })
      m.push({ label: 'Open data', Icon: Table2, key: 'F4', run: tab.grid })
      m.push({ label: 'New query', Icon: Terminal, run: tab.tableQuery })
      m.push({ label: 'Generate', Icon: Code, children: [
        { label: 'SELECT', Icon: Code, run: () => generate('select') },
        { label: 'INSERT', Icon: Code, run: () => generate('insert') },
        { label: 'UPDATE', Icon: Code, run: () => generate('update') },
        { label: 'CREATE (DDL)', Icon: Code, run: () => generate('create') },
      ] })
      m.push({ label: 'Export', Icon: Download, children: [
        { label: 'CSV', Icon: Download, run: () => exportAs('csv') },
        { label: 'JSON', Icon: Download, run: () => exportAs('json') },
      ] })
      m.push(SEP)
      m.push({ label: 'Copy name', Icon: Copy, run: () => app.copy(c.table!) })
      m.push({ label: 'Copy qualified name', Icon: Copy, run: () => app.copy(`${c.schema}.${c.table}`) })
      m.push({ label: 'Copy DDL', Icon: FileCode, run: () => generate('create') })
      m.push(SEP)
      m.push({ label: 'Quick documentation', Icon: Info, key: 'Ctrl+Q', run: tab.doc })
      m.push({ label: 'Find usages', Icon: Search, key: 'Alt+F7', run: tab.usages })
      if (w) {
        m.push(SEP)
        m.push({ label: 'Edit table…', Icon: Pencil, key: 'Ctrl+F6', run: () => app.openTableDesigner({ connId: id, schema: c.schema!, table: c.table!, mode: 'modify' }) })
        m.push({ label: 'Modify table', Icon: Pencil, children: [
          { label: 'Add column…', Icon: Plus, run: () => form('add-column') },
          { label: 'Create index…', Icon: ListTree, run: () => form('new-index') },
        ] })
        m.push({ label: 'Rename…', Icon: Pencil, run: () => form('rename-table') })
        m.push({ label: 'Truncate…', Icon: Eraser, danger: true, run: () => confirmRun(`Truncate ${c.schema}.${c.table}? This deletes ALL rows.`, () => api.truncate(id, c.schema!, c.table!)) })
      }
      if (a) m.push({ label: 'Drop table…', Icon: Trash2, danger: true, run: () => confirmRun(`Drop table ${c.schema}.${c.table}? This cannot be undone.`, () => api.dropTable(id, c.schema!, c.table!), true) })
      m.push(SEP)
      m.push({ label: 'Refresh', Icon: RotateCw, run: () => { close(); app.refreshConn(id) } })
    } else if (c.type === 'col') {
      m.push({ head: c.name })
      m.push({ label: 'Copy name', Icon: Copy, run: () => app.copy(c.name) })
      m.push({ label: 'Copy qualified name', Icon: Copy, run: () => app.copy(`${c.schema}.${c.table}.${c.name}`) })
      if (w) {
        m.push(SEP)
        m.push({ label: 'Modify column…', Icon: Pencil, run: () => form('modify-column', c.name) })
      }
      if (a) m.push({ label: 'Drop column…', Icon: Trash2, danger: true, run: () => confirmRun(`Drop column ${c.name} from ${c.schema}.${c.table}?`, () => api.dropColumn(id, c.schema!, c.table!, c.name), true) })
    } else if (c.type === 'key' || c.type === 'index') {
      m.push({ head: c.name })
      m.push({ label: 'Copy name', Icon: Copy, run: () => app.copy(c.name) })
      if (c.def) m.push({ label: 'Copy definition', Icon: Copy, run: () => app.copy(c.def!) })
      if (c.type === 'index' && a) {
        m.push(SEP)
        m.push({ label: 'Drop index…', Icon: Trash2, danger: true, run: () => confirmRun(`Drop index ${c.name}?`, () => api.dropIndex(id, c.schema!, c.name), true) })
      }
    }
    return m
  }

  function exportAs(format: string) {
    close()
    api.exportTable(id, payload.schema!, payload.table!, '', '', format).catch(e => app.notify(String(e.message || e), 'error'))
  }

  // Dropping a schema needs a second decision: CASCADE (take contained objects
  // with it) vs RESTRICT (fail if not empty). A plain DROP errors on any
  // non-empty schema, so offer both explicitly rather than silently picking one.
  async function dropSchema(schema: string) {
    const choice = await app.confirm({
      title: `Drop schema “${schema}”?`,
      body: 'This cannot be undone. RESTRICT fails if the schema still contains objects; CASCADE drops the schema and everything inside it (tables, views, …).',
      buttons: [
        { label: 'Drop (restrict)', value: 'restrict', variant: 'danger' },
        { label: 'Drop with CASCADE', value: 'cascade', variant: 'danger' },
      ],
    })
    if (!choice) return
    api.dropSchema(id, schema, choice === 'cascade').then(() => app.refreshConn(id)).catch(e => app.notify(String(e.message || e), 'error'))
  }

  const onItem = (it: MenuItem, i: number) => {
    if (it.children) { setOpenSub(s => s === i ? null : i); return }
    if (it.run) { it.run(); close() }
  }

  // On phones the menu becomes a full-width bottom sheet (see .ctx-menu.sheet in
  // hud.css); skip the desktop fly-out positioning so it never runs off-screen.
  const sheet = window.matchMedia('(max-width: 900px)').matches
  const left = Math.max(8, Math.min(x, window.innerWidth - MW - 8))
  const top = Math.max(8, Math.min(y, window.innerHeight - MH - 8))
  const flip = left + MW + 220 > window.innerWidth

  return (
    <>
      <div className="ctx-backdrop" onClick={close} onContextMenu={e => { e.preventDefault(); close() }} />
      <div ref={ref} className={`ctx-menu${sheet ? ' sheet' : ''}`} style={sheet ? undefined : { left, top }}>
        {items.map((it, i) => (
          <div key={i} className="menu-row">
            {it.sep ? <div className="menu-sep" />
              : it.head ? <div className="ctx-head">{it.head}</div>
              : (
                <button type="button"
                  className={`menu-item${it.children ? ' has-children' : ''}${it.danger ? ' danger' : ''}${openSub === i ? ' expanded' : ''}`}
                  onClick={() => onItem(it, i)}>
                  {it.Icon && <span className="mi-ico"><it.Icon size={15} /></span>}
                  <span className="mi-label">{it.label}</span>
                  {it.key && <span className="ctx-key">{it.key}</span>}
                  {it.children && <span className="mi-caret"><ChevronRight size={13} /></span>}
                </button>
              )}
            {it.children && openSub === i && (
              <div className={`ctx-submenu${sheet ? ' sheet' : flip ? ' flip' : ''}`}>
                {it.children.map((s, j) => (
                  <button key={j} type="button" className={`menu-item${s.danger ? ' danger' : ''}`}
                    onClick={() => { s.run && s.run(); close() }}>
                    {s.Icon && <span className="mi-ico"><s.Icon size={15} /></span>}
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
