import { useEffect, useState } from 'react'
import { api } from '../api'
import { useApp, type DDLKind, type NodePayload } from '../appctx'

interface MenuItem {
  label?: string
  icon?: string
  glyph?: string
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
// old Alpine buildMenu().
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
      m.push({ label: 'Query console', icon: 'ico ico-console', key: 'Ctrl+⏎', run: tab.console })
      if (w) m.push({ label: 'New…', glyph: '＋', children: [
        { label: 'Query console', icon: 'ico ico-console', run: tab.console },
        { label: 'Schema…', glyph: '▤', run: () => form('new-schema') },
      ] })
      m.push({ label: 'Refresh', glyph: '↻', key: 'Ctrl+F5', run: () => { close(); app.refreshConn(id) } })
      m.push(SEP)
      m.push({ label: 'Copy name', glyph: '⎘', run: () => app.copy(c.name) })
      if (a) {
        m.push({ label: 'Properties', glyph: '⚙', key: 'F4', run: () => app.openEditModal(id) })
        m.push({ label: 'Duplicate…', glyph: '⎘', run: () => app.openEditModal(id) })
        m.push(SEP)
        m.push({ label: 'Remove data source', glyph: '✕', key: 'Del', danger: true, run: () => confirmRun(`Remove data source “${c.name}”?`, () => api.deleteConnection(id).then(app.reloadConns)) })
      }
    } else if (c.type === 'schema') {
      m.push({ head: c.schema! })
      m.push({ label: 'Query console', icon: 'ico ico-console', run: tab.console })
      if (w) m.push({ label: 'New…', glyph: '＋', children: [{ label: 'Table…', glyph: '▦', run: () => form('new-table') }] })
      m.push({ label: 'Refresh', glyph: '↻', key: 'Ctrl+F5', run: () => { close(); app.refreshConn(id) } })
      m.push(SEP)
      m.push({ label: 'Copy name', glyph: '⎘', run: () => app.copy(c.schema!) })
    } else if (c.type === 'table') {
      m.push({ head: `${c.schema}.${c.table}` })
      m.push({ label: 'Open data', icon: 'ico ico-table', key: 'F4', run: tab.grid })
      m.push({ label: 'Generate', glyph: '▷', children: [
        { label: 'SELECT', glyph: '▷', run: () => generate('select') },
        { label: 'INSERT', glyph: '▷', run: () => generate('insert') },
        { label: 'UPDATE', glyph: '▷', run: () => generate('update') },
        { label: 'CREATE (DDL)', glyph: '▷', run: () => generate('create') },
      ] })
      m.push({ label: 'Export', glyph: '⭳', children: [
        { label: 'CSV', glyph: '⭳', run: () => exportAs('csv') },
        { label: 'JSON', glyph: '⭳', run: () => exportAs('json') },
      ] })
      m.push(SEP)
      m.push({ label: 'Copy name', glyph: '⎘', run: () => app.copy(c.table!) })
      m.push({ label: 'Copy qualified name', glyph: '⎘', run: () => app.copy(`${c.schema}.${c.table}`) })
      m.push({ label: 'Copy DDL', glyph: '❑', run: () => generate('create') })
      m.push(SEP)
      m.push({ label: 'Quick documentation', glyph: 'ⓘ', key: 'Ctrl+Q', run: tab.doc })
      m.push({ label: 'Find usages', glyph: '⌕', key: 'Alt+F7', run: tab.usages })
      if (w) {
        m.push(SEP)
        m.push({ label: 'Modify table', glyph: '✎', children: [
          { label: 'Add column…', glyph: '＋', run: () => form('add-column') },
          { label: 'Create index…', glyph: '⊞', run: () => form('new-index') },
        ] })
        m.push({ label: 'Rename…', glyph: '✎', run: () => form('rename-table') })
        m.push({ label: 'Truncate…', glyph: '∅', danger: true, run: () => confirmRun(`Truncate ${c.schema}.${c.table}? This deletes ALL rows.`, () => api.truncate(id, c.schema!, c.table!)) })
      }
      if (a) m.push({ label: 'Drop table…', glyph: '✕', danger: true, run: () => confirmRun(`Drop table ${c.schema}.${c.table}? This cannot be undone.`, () => api.dropTable(id, c.schema!, c.table!), true) })
      m.push(SEP)
      m.push({ label: 'Refresh', glyph: '↻', run: () => { close(); app.refreshConn(id) } })
    } else if (c.type === 'col') {
      m.push({ head: c.name })
      m.push({ label: 'Copy name', glyph: '⎘', run: () => app.copy(c.name) })
      m.push({ label: 'Copy qualified name', glyph: '⎘', run: () => app.copy(`${c.schema}.${c.table}.${c.name}`) })
      if (w) {
        m.push(SEP)
        m.push({ label: 'Modify column…', glyph: '✎', run: () => form('modify-column', c.name) })
      }
      if (a) m.push({ label: 'Drop column…', glyph: '✕', danger: true, run: () => confirmRun(`Drop column ${c.name} from ${c.schema}.${c.table}?`, () => api.dropColumn(id, c.schema!, c.table!, c.name), true) })
    } else if (c.type === 'key' || c.type === 'index') {
      m.push({ head: c.name })
      m.push({ label: 'Copy name', glyph: '⎘', run: () => app.copy(c.name) })
      if (c.def) m.push({ label: 'Copy definition', glyph: '⎘', run: () => app.copy(c.def!) })
      if (c.type === 'index' && a) {
        m.push(SEP)
        m.push({ label: 'Drop index…', glyph: '✕', danger: true, run: () => confirmRun(`Drop index ${c.name}?`, () => api.dropIndex(id, c.schema!, c.name), true) })
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
                  {it.glyph && <span className="mi-ico">{it.glyph}</span>}
                  {it.icon && <span className={it.icon} />}
                  <span className="mi-label">{it.label}</span>
                  {it.key && <span className="ctx-key">{it.key}</span>}
                  {it.children && <span className="mi-caret">▸</span>}
                </button>
              )}
            {it.children && openSub === i && (
              <div className={`ctx-submenu${flip ? ' flip' : ''}`}>
                {it.children.map((s, j) => (
                  <button key={j} type="button" className={`menu-item${s.danger ? ' danger' : ''}`}
                    onClick={() => { s.run && s.run(); close() }}>
                    {s.glyph && <span className="mi-ico">{s.glyph}</span>}
                    {s.icon && <span className={s.icon} />}
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
