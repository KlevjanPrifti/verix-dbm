// Central icon set every glyph in the app is a Lucide icon, so the UI stays
// visually consistent and crisp at any zoom (SVG, not font glyphs).
//
// Two surfaces:
//   • <Ico name="…" /> type/category icons for the Explorer tree + designer.
//     Colour comes from the `.ico-<name>` CSS classes (theme-aware), the SVG
//     inherits it via currentColor.
//   • The re-exported Lucide components below used directly for toolbar
//     buttons, the context menu, tabs, etc.

import type { LucideIcon } from 'lucide-react'
import {
  Database, Box, Table2, Eye, Folder, KeyRound, Key, Link2, Hash, Type, Clock,
  ToggleLeft, Braces, Fingerprint, ListTree, Terminal, Columns3, Circle,
  User, Users,
} from 'lucide-react'
import {
  SiPostgresql, SiCockroachlabs, SiMysql, SiMariadb, SiRedis, SiTimescale,
} from 'react-icons/si'
import { FaAws } from 'react-icons/fa'
import { DB_KINDS } from './dbkinds'

// Any icon component here needs only className/style; both Lucide glyphs and
// react-icons brand logos satisfy it (the brand logos are solid fills that pick
// up colour from currentColor, so a single accent tints the whole silhouette).
type IconCmp = React.ComponentType<{ className?: string; style?: React.CSSProperties }>

// Real brand logos for the database kinds that have one. Redshift and Aurora
// have no product mark in the icon set, so they borrow the AWS logo (both are
// AWS services). Greenplum and Yugabyte have no published mark anywhere and fall
// back to the generic Database cylinder below.
const BRAND: Record<string, IconCmp> = {
  postgres: SiPostgresql,
  cockroach: SiCockroachlabs,
  timescale: SiTimescale,
  mysql: SiMysql,
  mariadb: SiMariadb,
  redis: SiRedis,
  redshift: FaAws,
  aurorapg: FaAws,
}

// Database-object types → icon. Keys match Explorer's dynamic names: connection
// kinds (postgres/redis/…, sourced from the dbkinds registry), table kinds
// (table/view/matview), column categories (num/text/time/bool/json/uuid/pk) and
// the structural folders. Connection kinds use their brand logo when known.
const TYPE_ICONS: Record<string, IconCmp> = {
  ...Object.fromEntries(DB_KINDS.map(k => [k.id, BRAND[k.id] || Database])),
  keyspace: SiRedis,
  schema: Box, table: Table2, view: Eye, matview: Eye, folder: Folder,
  col: Columns3, pk: KeyRound, key: Key, fkey: Link2, idx: ListTree,
  num: Hash, text: Type, time: Clock, bool: ToggleLeft, json: Braces,
  uuid: Fingerprint, console: Terminal, roles: Users, role: User,
}

export function Ico({ name, className, color }: { name: string; className?: string; color?: string }) {
  const C = TYPE_ICONS[name] || Circle
  return <C className={`ico ico-${name}${className ? ' ' + className : ''}`} style={color ? { color } : undefined} aria-hidden />
}

// nameColor derives a stable accent colour from a connection's name so two
// connections of the same kind stay distinguishable at a glance in the tree.
// FNV-1a hash → hue; saturation/lightness are fixed for legibility on the dark
// HUD theme. The same name always yields the same colour.
export function nameColor(name: string): string {
  let h = 0x811c9dc5
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return `hsl(${(h >>> 0) % 360} 70% 66%)`
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
  TableProperties, UserPlus, User, Users,
} from 'lucide-react'
