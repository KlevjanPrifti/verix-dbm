import { createContext, useContext } from 'react'
import type { Connection, Role } from './types'

// Tree node identity, sourced from the explorer and fed into the context menu —
// the React equivalent of the data-* attributes the old Alpine menu read.
export interface NodePayload {
  type: 'conn' | 'schema' | 'table' | 'col' | 'key' | 'index' | 'roles' | 'role'
  connId: number
  name: string
  schema?: string
  table?: string
  kind?: string
  def?: string
  role?: Role // role nodes: full attributes, for the edit form to prefill from
}

export type TabView =
  | { type: 'grid'; connId: number; schema: string; table: string }
  | { type: 'console'; connId: number; sql?: string }
  | { type: 'redis'; connId: number }
  | { type: 'doc'; connId: number; schema: string; table: string }
  | { type: 'usages'; connId: number; schema: string; table: string }

export interface TabDef {
  key: string
  title: string
  icon: 'grid' | 'console'
  view: TabView
}

export type DDLKind =
  | 'add-column' | 'modify-column' | 'rename-table'
  | 'new-schema' | 'new-table' | 'new-index' | 'create-user'
  | 'alter-schema' | 'alter-user'

export interface DDLParams {
  connId: number
  kind: DDLKind
  schema: string
  table: string
  column?: string
  role?: Role // alter-user: the role being edited, used to prefill the form
}

// Full DataGrip-style table editor — "create" builds a new table, "modify" loads
// an existing one and emits a diff of ALTERs. Distinct from DDLParams (the small
// single-field forms) because it drives a much larger, structured dialog.
export interface TableDesignerParams {
  connId: number
  schema: string
  mode: 'create' | 'modify'
  table?: string
}

export interface Caps { admin: boolean; write: boolean; csrf: string }

// AppActions is the imperative surface every component reaches for via useApp().
export interface AppActions {
  caps: Caps
  conns: Connection[]
  connById: (id: number) => Connection | undefined
  openTab: (t: TabDef) => void
  closeTab: (key: string) => void
  copy: (text: string) => void
  notify: (msg: string, kind?: 'ok' | 'error') => void
  refreshConn: (id: number) => void
  refreshToken: (id: number) => number // bump as a refetch dependency for a conn's subtree
  openCtx: (x: number, y: number, payload: NodePayload) => void
  openConnModal: (kind?: string) => void
  openEditModal: (id: number) => void
  openDDL: (p: DDLParams) => void
  openTableDesigner: (p: TableDesignerParams) => void
  reloadConns: () => void
}

export const AppContext = createContext<AppActions>(null as unknown as AppActions)
export const useApp = () => useContext(AppContext)
