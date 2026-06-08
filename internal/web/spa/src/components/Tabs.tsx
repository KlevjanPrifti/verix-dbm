import { useApp, type TabDef } from '../appctx'
import GridTab from './tabs/GridTab'
import ConsoleTab from './tabs/ConsoleTab'
import RedisTab from './tabs/RedisTab'
import DocTab from './tabs/DocTab'
import UsagesTab from './tabs/UsagesTab'

// The tabbed workspace. Tabs stay mounted (hidden when inactive) so console
// text and grid filters survive switching — the previous HTMX shell reloaded
// each tab on select and lost that state.
export default function Tabs({ tabs, active, onSelect, onClose, onToggleDrawer }: {
  tabs: TabDef[]
  active: string | null
  onSelect: (key: string) => void
  onClose: (key: string) => void
  onToggleDrawer: () => void
}) {
  return (
    <section className="work">
      <div className="tabbar">
        <button type="button" className="drawer-toggle" title="Database Explorer"
          aria-label="Toggle Database Explorer" onClick={onToggleDrawer}>☰</button>
        {tabs.map(t => (
          <div key={t.key} className={`tab${active === t.key ? ' on' : ''}`} onClick={() => onSelect(t.key)}>
            <span className={`tab-ico ti-${t.icon}`} />
            <span className="tab-title">{t.title}</span>
            <button type="button" className="tab-x" onClick={e => { e.stopPropagation(); onClose(t.key) }}>✕</button>
          </div>
        ))}
        {tabs.length === 0 && (
          <span className="tab-hint hud-label dim">double-click a table to browse · ⌨ opens a console</span>
        )}
      </div>

      <div className="tab-content">
        {tabs.length === 0 && (
          <div className="welcome">
            <div className="welcome-box hud-panel p-4">
              <div className="hud-heading">VERIX<span className="cta">DBM</span></div>
              <p className="dim">Expand a connection in the Database Explorer, double-click a table to browse it,
                or open a query console with the ⌨ button on a connection.</p>
            </div>
          </div>
        )}
        {tabs.map(t => (
          <div key={t.key} style={{ display: active === t.key ? 'flex' : 'none', flex: 1, minHeight: 0 }}>
            <TabContent tab={t} />
          </div>
        ))}
      </div>
    </section>
  )
}

function TabContent({ tab }: { tab: TabDef }) {
  const app = useApp()
  const v = tab.view
  switch (v.type) {
    case 'grid': return <GridTab connId={v.connId} schema={v.schema} table={v.table} />
    case 'console': {
      const c = app.connById(v.connId)
      return c?.kind === 'redis' ? <RedisTab connId={v.connId} /> : <ConsoleTab connId={v.connId} />
    }
    case 'redis': return <RedisTab connId={v.connId} />
    case 'doc': return <DocTab connId={v.connId} schema={v.schema} table={v.table} />
    case 'usages': return <UsagesTab connId={v.connId} schema={v.schema} table={v.table} />
  }
}
