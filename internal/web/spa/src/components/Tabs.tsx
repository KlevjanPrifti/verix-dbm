import { useEffect, useRef, useState } from 'react'
import { useApp, type TabDef } from '../appctx'
import { Copy, PanelLeft, Table2, Terminal, X } from '../icons'
import GridTab from './tabs/GridTab'
import ConsoleTab from './tabs/ConsoleTab'
import RedisTab from './tabs/RedisTab'
import MongoTab from './tabs/MongoTab'
import DocTab from './tabs/DocTab'
import UsagesTab from './tabs/UsagesTab'

// The tabbed workspace. Tabs stay mounted (hidden when inactive) so console
// text and grid filters survive switching the previous HTMX shell reloaded
// each tab on select and lost that state.
export default function Tabs({ tabs, active, onSelect, onClose, onCloseOthers, onCloseAll, onCloseRight, onToggleDrawer }: {
  tabs: TabDef[]
  active: string | null
  onSelect: (key: string) => void
  onClose: (key: string) => void
  onCloseOthers: (key: string) => void
  onCloseAll: () => void
  onCloseRight: (key: string) => void
  onToggleDrawer: () => void
}) {
  const [menu, setMenu] = useState<{ x: number; y: number; tab: TabDef } | null>(null)
  return (
    <section className="work">
      <div className="tabbar">
        <button type="button" className="drawer-toggle" title="Database Explorer"
          aria-label="Toggle Database Explorer" onClick={onToggleDrawer}><PanelLeft size={18} /></button>
        {tabs.map(t => (
          <div key={t.key} className={`tab${active === t.key ? ' on' : ''}`} onClick={() => onSelect(t.key)}
            onContextMenu={e => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY, tab: t }) }}>
            <span className="tab-ico">{t.icon === 'console' ? <Terminal size={13} /> : <Table2 size={13} />}</span>
            <span className="tab-title">{t.title}</span>
            <button type="button" className="tab-x" onClick={e => { e.stopPropagation(); onClose(t.key) }}><X size={13} /></button>
          </div>
        ))}
        {tabs.length === 0 && (
          <span className="tab-hint hud-label dim">double-click a table to browse · <Terminal size={12} /> opens a console</span>
        )}
      </div>

      <div className="tab-content">
        {tabs.length === 0 && (
          <div className="welcome">
            <div className="welcome-box hud-panel p-4">
              <div className="hud-heading">VERIX<span className="cta">DBM</span></div>
              <p className="dim">Expand a connection in the Database Explorer, double-click a table to browse it,
                or open a query console with the <Terminal size={13} /> button on a connection.</p>
            </div>
          </div>
        )}
        {tabs.map(t => (
          <div key={t.key} style={{ display: active === t.key ? 'flex' : 'none', flexDirection: 'column', flex: 1, minHeight: 0, minWidth: 0 }}>
            <TabContent tab={t} />
          </div>
        ))}
      </div>

      {menu && (
        <TabMenu
          x={menu.x} y={menu.y} tab={menu.tab} tabs={tabs}
          onClose={() => setMenu(null)}
          onCloseTab={onClose} onCloseOthers={onCloseOthers}
          onCloseAll={onCloseAll} onCloseRight={onCloseRight}
        />
      )}
    </section>
  )
}

const MW = 230, MH = 240

// TabMenu is the right-click menu on a workspace tab: close
// actions plus copy helpers. Reuses the tree menu's .ctx-menu styling.
function TabMenu({ x, y, tab, tabs, onClose, onCloseTab, onCloseOthers, onCloseAll, onCloseRight }: {
  x: number; y: number; tab: TabDef; tabs: TabDef[]
  onClose: () => void
  onCloseTab: (key: string) => void
  onCloseOthers: (key: string) => void
  onCloseAll: () => void
  onCloseRight: (key: string) => void
}) {
  const app = useApp()
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
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

  const i = tabs.findIndex(t => t.key === tab.key)
  const hasOthers = tabs.length > 1
  const hasRight = i >= 0 && i < tabs.length - 1
  const v = tab.view
  const qualified = 'table' in v ? `${v.schema}.${v.table}` : undefined

  const items: ({ sep: true } | { label: string; key?: string; danger?: boolean; disabled?: boolean; Icon?: typeof X; run: () => void })[] = [
    { label: 'Close', key: 'Ctrl+F4', Icon: X, run: () => onCloseTab(tab.key) },
    { label: 'Close Other Tabs', disabled: !hasOthers, run: () => onCloseOthers(tab.key) },
    { label: 'Close Tabs to the Right', disabled: !hasRight, run: () => onCloseRight(tab.key) },
    { label: 'Close All Tabs', run: onCloseAll },
    { sep: true },
    { label: 'Copy Name', Icon: Copy, run: () => app.copy(tab.title) },
    ...(qualified ? [{ label: 'Copy Qualified Name', Icon: Copy, run: () => app.copy(qualified) }] : []),
  ]

  const sheet = window.matchMedia('(max-width: 900px)').matches
  const left = Math.max(8, Math.min(x, window.innerWidth - MW - 8))
  const top = Math.max(8, Math.min(y, window.innerHeight - MH - 8))

  return (
    <>
      <div className="ctx-backdrop" onClick={onClose} onContextMenu={e => { e.preventDefault(); onClose() }} />
      <div ref={ref} className={`ctx-menu${sheet ? ' sheet' : ''}`} style={sheet ? undefined : { left, top }}>
        {items.map((it, k) => 'sep' in it ? <div key={k} className="menu-sep" /> : (
          <button key={k} type="button" className={`menu-item${it.danger ? ' danger' : ''}`}
            disabled={it.disabled}
            onClick={() => { if (!it.disabled) { it.run(); onClose() } }}>
            {it.Icon ? <span className="mi-ico"><it.Icon size={15} /></span> : <span className="mi-ico" />}
            <span className="mi-label">{it.label}</span>
            {it.key && <span className="ctx-key">{it.key}</span>}
          </button>
        ))}
      </div>
    </>
  )
}

function TabContent({ tab }: { tab: TabDef }) {
  const app = useApp()
  const v = tab.view
  switch (v.type) {
    case 'grid': return <GridTab connId={v.connId} schema={v.schema} table={v.table} />
    case 'console': {
      const c = app.connById(v.connId)
      return c?.kind === 'redis' ? <RedisTab connId={v.connId} /> : <ConsoleTab connId={v.connId} initialSql={v.sql} schema={v.schema} />
    }
    case 'redis': return <RedisTab connId={v.connId} />
    case 'mongo': return <MongoTab connId={v.connId} db={v.db} coll={v.coll} />
    case 'doc': return <DocTab connId={v.connId} schema={v.schema} table={v.table} />
    case 'usages': return <UsagesTab connId={v.connId} schema={v.schema} table={v.table} />
  }
}
