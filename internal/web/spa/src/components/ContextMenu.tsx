import { useEffect, useState } from 'react'
import { api } from '../api'
import { useApp, type DDLKind, type NodePayload } from '../appctx'
import {
  type LucideIcon, Terminal, Plus, Box, RotateCw, Copy, Settings, Trash2, Table2,
  Code, Download, FileCode, Info, Search, Pencil, ListTree, Eraser, ChevronRight,
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

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const close = onClose
  const tab = {
    console: () => app.openTab({ key: `console:${id}`, title: `console [${payload.name}]`, icon: 'console', view: { type: 'console', connId: id } }),
    grid: () => app.openTab({ key: `grid:${id}:${payload.schema}.${payload.table}`, title: `${payload.schema}.${payload.table}`, icon: 'grid', view: { type: 'grid', connId: id, schema: payload.schema!, table: payload.table! } }),
    doc: () => app.openTab({ key: `doc:${id}:${payload.schema}.${payload.table}`, title: `doc [${payload.table}]`, icon: 'grid', view: { type: 'doc', connId: id, schema: payload.schema!, table: payload.table! } }),
    usages: () => app.openTab({ key: `usages:${id}:${payload.schema}.${payload.table}`, title: `usages [${payload.table}]`, icon: 'grid', view: { type: 'usages', connId: id, schema: payload.schema!, table: payload.table! } }),
  }
  const generate = (kind: string) =>
    api.generate(id, kind, payload.schema!, payload.table!).then(r => app.copy(r.sql)).catch(e => app.notify(String(e.message || e), 'error'))
  const form = (kind: DDLKind, column?: string) => app.openDDL({ connId: id, kind, schema: payload.schema || '', table: payload.table || '', column })
  const confirmRun = (msg: string, run: () => Promise<unknown>, refresh = false) => {
    if (!confirm(msg)) return
    run().then(() => { if (refresh) app.refreshConn(id) }).catch(e => app.notify(String(e.message || e), 'error'))
  }

  const items = buildMenu()
  function buildMenu(): MenuItem[] {
    const w = app.caps.write, a = app.caps.admin
    const c = payload
    const m: MenuItem[] = []
    if (c.type === 'conn') {
      m.push({ head: c.name })
      m.push({ label: 'Query console', Icon: Terminal, key: 'Ctrl+⏎', run: tab.console })
      if (w) m.push({ label: 'New…', Icon: Plus, children: [
        { label: 'Query console', Icon: Terminal, run: tab.console },
        { label: 'Schema…', Icon: Box, run: () => form('new-schema') },
      ] })
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
      m.push({ label: 'Refresh', Icon: RotateCw, key: 'Ctrl+F5', run: () => { close(); app.refreshConn(id) } })
      m.push(SEP)
      m.push({ label: 'Copy name', Icon: Copy, run: () => app.copy(c.schema!) })
    } else if (c.type === 'table') {
      m.push({ head: `${c.schema}.${c.table}` })
      m.push({ label: 'Open data', Icon: Table2, key: 'F4', run: tab.grid })
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

  const onItem = (it: MenuItem, i: number) => {
    if (it.children) { setOpenSub(s => s === i ? null : i); return }
    if (it.run) { it.run(); close() }
  }

  const left = Math.max(8, Math.min(x, window.innerWidth - MW - 8))
  const top = Math.max(8, Math.min(y, window.innerHeight - MH - 8))
  const flip = left + MW + 220 > window.innerWidth

  return (
    <>
      <div className="ctx-backdrop" style={{ position: 'fixed', inset: 0, zIndex: 900 }} onClick={close} onContextMenu={e => { e.preventDefault(); close() }} />
      <div className="ctx-menu" style={{ left, top }}>
        {items.map((it, i) => (
          <div key={i} className="menu-row"
            onMouseEnter={() => it.children && setOpenSub(i)}
            onMouseLeave={() => it.children && setOpenSub(s => (s === i ? null : s))}>
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
              <div className={`ctx-submenu${flip ? ' flip' : ''}`}>
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
