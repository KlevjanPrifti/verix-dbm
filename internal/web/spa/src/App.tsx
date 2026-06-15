import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, setCSRF } from './api'
import { kindEngine } from './dbkinds'
import type { Connection, Me } from './types'
import {
  AppContext, type AppActions, type ConfirmSpec, type DDLParams, type DialogRequest,
  type NodePayload, type PromptSpec, type TabDef, type TableDesignerParams,
} from './appctx'
import Explorer from './components/Explorer'
import Tabs from './components/Tabs'
import ContextMenu from './components/ContextMenu'
import ConnModal from './components/ConnModal'
import DDLModal from './components/DDLModal'
import TableDesigner from './components/TableDesigner'
import AuditModal from './components/AuditModal'
import Dialog from './components/Dialog'
import Toasts, { type Notice } from './components/Toasts'
import { LogOut } from './icons'

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [conns, setConns] = useState<Connection[]>([])
  const [tabs, setTabs] = useState<TabDef[]>([])
  const [active, setActive] = useState<string | null>(null)
  const [ctx, setCtx] = useState<{ x: number; y: number; payload: NodePayload } | null>(null)
  const [connModal, setConnModal] = useState<{ mode: 'create'; kind: string } | { mode: 'edit'; id: number } | null>(null)
  const [ddl, setDDL] = useState<DDLParams | null>(null)
  const [designer, setDesigner] = useState<TableDesignerParams | null>(null)
  const [auditOpen, setAuditOpen] = useState(false)
  const [drawer, setDrawer] = useState(false)
  const [notices, setNotices] = useState<Notice[]>([])
  const [refreshTokens, setRefreshTokens] = useState<Record<number, number>>({})
  // One global HUD dialog at a time; `resolve` settles the promise the
  // confirm()/prompt() helpers handed back to the caller.
  const [dialog, setDialog] = useState<{ req: DialogRequest; resolve: (v: string | null) => void } | null>(null)
  const noticeId = useRef(0)

  const reloadConns = useCallback(() => {
    api.listConnections().then(r => setConns(r.connections)).catch(() => {})
  }, [])

  useEffect(() => {
    api.me().then(m => { setMe(m); setCSRF(m.csrf) }).catch(() => {})
    reloadConns()
  }, [reloadConns])

  const notify = useCallback((msg: string, kind: 'ok' | 'error' = 'ok') => {
    const id = ++noticeId.current
    setNotices(n => [...n, { id, msg, kind }])
    setTimeout(() => setNotices(n => n.filter(x => x.id !== id)), 3200)
  }, [])

  const copy = useCallback((text: string) => {
    if (navigator.clipboard) navigator.clipboard.writeText(text).then(() => notify('Copied')).catch(() => {})
  }, [notify])

  const confirm = useCallback((spec: ConfirmSpec) => new Promise<string | null>(resolve => {
    setDialog({
      req: {
        kind: 'confirm', title: spec.title, body: spec.body, cancelLabel: spec.cancelLabel,
        buttons: spec.buttons ?? [{ label: 'OK', value: 'ok', variant: 'cta' }],
      },
      resolve,
    })
  }), [])

  const prompt = useCallback((spec: PromptSpec) => new Promise<string | null>(resolve => {
    setDialog({ req: { kind: 'prompt', ...spec }, resolve })
  }), [])

  const openTab = useCallback((t: TabDef) => {
    setTabs(prev => prev.some(x => x.key === t.key) ? prev : [...prev, t])
    setActive(t.key)
    setDrawer(false)
  }, [])

  const closeTab = useCallback((key: string) => {
    setTabs(prev => {
      const i = prev.findIndex(t => t.key === key)
      if (i < 0) return prev
      const next = prev.filter(t => t.key !== key)
      setActive(cur => {
        if (cur !== key) return cur
        const fallback = next[i] || next[i - 1]
        return fallback ? fallback.key : null
      })
      return next
    })
  }, [])

  const refreshConn = useCallback((id: number) => {
    setRefreshTokens(t => ({ ...t, [id]: (t[id] || 0) + 1 }))
  }, [])

  const connById = useCallback((id: number) => conns.find(c => c.id === id), [conns])

  const activeView = useMemo(() => tabs.find(t => t.key === active)?.view ?? null, [tabs, active])

  const actions = useMemo<AppActions>(() => ({
    caps: { admin: me?.user.admin ?? false, write: me?.user.write ?? false, csrf: me?.csrf ?? '', scopedAccess: me?.scopedAccess ?? false },
    conns,
    activeView,
    connById,
    openTab,
    closeTab,
    copy,
    notify,
    confirm,
    prompt,
    refreshConn,
    refreshToken: (id: number) => refreshTokens[id] || 0,
    openCtx: (x, y, payload) => setCtx({ x, y, payload }),
    openConnModal: (kind = 'postgres') => setConnModal({ mode: 'create', kind }),
    openEditModal: (id) => setConnModal({ mode: 'edit', id }),
    openDDL: (p) => setDDL(p),
    openTableDesigner: (p) => setDesigner(p),
    reloadConns,
  }), [me, conns, activeView, connById, openTab, closeTab, copy, notify, confirm, prompt, refreshConn, refreshTokens, reloadConns])

  // Tint the HUD to match the active tab's connection engine
  // (cyan pg / amber mysql / emerald redis).
  useEffect(() => {
    const t = tabs.find(x => x.key === active)
    const c = t ? conns.find(x => x.id === ('connId' in t.view ? t.view.connId : -1)) : undefined
    const engine = c ? kindEngine(c.kind) : 'postgres'
    document.documentElement.dataset.accent = engine === 'redis' ? 'emerald' : engine === 'mysql' ? 'amber' : 'cyan'
  }, [active, tabs, conns])

  if (!me) return <div className="boot dim" style={{ padding: '2rem' }}>loading…</div>

  return (
    <AppContext.Provider value={actions}>
      <header className="topbar">
        <a className="brand hud-heading" href="/app">VERIX<span className="cta">DBM</span></a>
        <nav className="topnav hud-label">
          <a className="on" href="/app">Connections</a>
          {me.user.admin && <a href="#" onClick={e => { e.preventDefault(); setAuditOpen(true) }}>Audit</a>}
          {me.user.admin && <a href="#" onClick={async e => {
            e.preventDefault()
            const ok = await confirm({ title: 'Re-encrypt credentials', body: 'Re-encrypt every saved password under the current primary key. Safe to run anytime; run after rotating DBM_ENC_KEYS.', buttons: [{ label: 'Re-encrypt', value: 'go', variant: 'cta' }] })
            if (ok !== 'go') return
            try {
              const r = await api.reencrypt()
              notify(`Re-encrypt (key ${r.primaryKey}): ${r.rewritten} rewritten, ${r.checked} checked` + (r.failed ? `, ${r.failed} failed` : ''), r.failed ? 'error' : 'ok')
            } catch (x: any) { notify(String(x.message || x), 'error') }
          }}>Re-encrypt</a>}
        </nav>
        <div className="userbox hud-label">
          <span className="who">{me.user.name}{me.user.admin ? ' · ADMIN' : me.user.write ? ' · WRITE' : ' · READ'}</span>
          {/* POST + CSRF so a cross-site page can't force a logout. */}
          <form method="post" action="/auth/logout" className="logout-form">
            <input type="hidden" name="csrf" value={me.csrf} />
            <button type="submit" className="logout-link"><LogOut size={13} /> <span className="logout-txt">Logout</span></button>
          </form>
        </div>
      </header>

      <main className="container">
        <div className="ide">
          {drawer && <div className="drawer-backdrop" onClick={() => setDrawer(false)} />}
          <Explorer open={drawer} />
          <Tabs
            tabs={tabs}
            active={active}
            onSelect={setActive}
            onClose={closeTab}
            onToggleDrawer={() => setDrawer(d => !d)}
          />
        </div>
      </main>

      {ctx && <ContextMenu x={ctx.x} y={ctx.y} payload={ctx.payload} onClose={() => setCtx(null)} />}
      {connModal && (
        <ConnModal
          mode={connModal.mode}
          editId={connModal.mode === 'edit' ? connModal.id : undefined}
          initialKind={connModal.mode === 'create' ? connModal.kind : undefined}
          onClose={() => setConnModal(null)}
          onSaved={() => { setConnModal(null); reloadConns() }}
        />
      )}
      {ddl && <DDLModal params={ddl} onClose={() => setDDL(null)} onApplied={() => { refreshConn(ddl.connId); setDDL(null) }} />}
      {designer && <TableDesigner params={designer} onClose={() => setDesigner(null)} onApplied={() => { refreshConn(designer.connId); setDesigner(null) }} />}
      {auditOpen && <AuditModal onClose={() => setAuditOpen(false)} />}
      {dialog && <Dialog req={dialog.req} onResolve={v => { dialog.resolve(v); setDialog(null) }} />}
      <Toasts notices={notices} />
    </AppContext.Provider>
  )
}
