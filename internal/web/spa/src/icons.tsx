// Central icon set — every glyph in the app is a Lucide icon, so the UI stays
// visually consistent and crisp at any zoom (SVG, not font glyphs).
//
// Two surfaces:
//   • <Ico name="…" /> — type/category icons for the Explorer tree + designer.
//     Colour comes from the `.ico-<name>` CSS classes (theme-aware), the SVG
//     inherits it via currentColor.
//   • The re-exported Lucide components below — used directly for toolbar
//     buttons, the context menu, tabs, etc.

import type { LucideIcon } from 'lucide-react'
import {
  Database, Box, Table2, Eye, Folder, KeyRound, Key, Link2, Hash, Type, Clock,
  ToggleLeft, Braces, Fingerprint, ListTree, Terminal, Columns3, Circle,
} from 'lucide-react'
import { DB_KINDS } from './dbkinds'

// Database-object types → icon. Keys match Explorer's dynamic names: connection
// kinds (postgres/redis/…, sourced from the dbkinds registry), table kinds
// (table/view/matview), column categories (num/text/time/bool/json/uuid/pk) and
// the structural folders. Every connection kind renders the Database glyph.
const TYPE_ICONS: Record<string, LucideIcon> = {
  ...Object.fromEntries(DB_KINDS.map(k => [k.id, Database])),
  keyspace: Database,
  schema: Box, table: Table2, view: Eye, matview: Eye, folder: Folder,
  col: Columns3, pk: KeyRound, key: Key, fkey: Link2, idx: ListTree,
  num: Hash, text: Type, time: Clock, bool: ToggleLeft, json: Braces,
  uuid: Fingerprint, console: Terminal,
}

export function Ico({ name, className }: { name: string; className?: string }) {
  const C = TYPE_ICONS[name] || Circle
  return <C className={`ico ico-${name}${className ? ' ' + className : ''}`} aria-hidden />
}

export type { LucideIcon }
export {
  // tree / structural
  Database, Box, Table2, Eye, Folder, KeyRound, Key, Link2, ListTree, Terminal,
  Columns3,
  // actions / chrome
  X, Plus, Minus, MoreHorizontal, RotateCw, PanelLeft, Play, ChevronUp,
  ChevronDown, ChevronLeft, ChevronRight, ArrowUp, ArrowDown, ArrowRight, Check,
  Copy, Settings, Trash2, Download, Code, FileCode, FileJson, Info, Search,
  Pencil, SquarePen, Eraser, LogOut, Filter, FilterX, Maximize2, Sigma, Undo2,
  TableProperties, UserPlus,
} from 'lucide-react'
