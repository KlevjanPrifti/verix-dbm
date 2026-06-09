// Mirrors the JSON DTOs emitted by internal/web/api.go.

export interface Me {
  user: { name: string; email: string; admin: boolean; write: boolean }
  csrf: string
}

export interface Connection {
  id: number
  name: string
  kind: string // a DB_KINDS id — see dbkinds.ts (postgres/cockroach/redshift/redis/…)
  host: string
  port: number
  dbname: string
  username: string
  options: string
  readOnly: boolean
}

export interface Table {
  Schema: string
  Name: string
  Kind: string // table | view | matview
  EstRows: number
}

export interface Schema {
  Name: string
  Tables: Table[] | null
}

export interface ExplorerData {
  kind: string
  schemas?: Schema[] | null
  error?: string
}

// Mirrors postgres.Role — a cluster role/user with its privilege attributes.
export interface Role {
  Name: string
  Super: boolean
  CreateDB: boolean
  CreateRole: boolean
  CanLogin: boolean
  Replication: boolean
  ConnLimit: number
  ValidUntil: string
}

export interface Column {
  name: string
  type: string
  typeText: string
  cat: string
  notNull: boolean
  default: string
  pk: boolean
  autoInc: boolean
}

export interface Index {
  Name: string
  Unique: boolean
  Primary: boolean
  Def: string
  Cols: string
}

export interface Key {
  Name: string
  Type: string // primary | foreign | unique | check | other
  Def: string
  Cols: string
}

export interface Usage {
  Schema: string
  Table: string
  Name: string
  Def: string
}

export interface QueryResult {
  columns: string[] | null
  rows: string[][] | null
  isSelect: boolean
  rowsAffected: number
  command: string
  duration: string
  truncated: boolean
}

export interface GridResponse {
  result: QueryResult | null
  readOnly: boolean
  page: number
  error?: string
}

export interface QueryResponse {
  result?: QueryResult | null
  readOnly: boolean
  needConfirm?: boolean
  sql?: string
  error?: string
}

export interface DocResponse {
  schema: string
  table: string
  columns: Column[]
  keys: Key[] | null
  indexes: Index[] | null
  comment: string
}

export interface UsagesResponse {
  schema: string
  table: string
  usages: Usage[] | null
  error?: string
}

export interface RedisKeyInfo {
  Key: string
  Type: string
  TTL: string
}

export interface RedisKeysResponse {
  keys: RedisKeyInfo[] | null
  cursor: number
}

export interface RedisValue {
  Key: string
  Type: string
  TTL: string
  Text: string
  Pairs: [string, string][] | null
  List: string[] | null
}

export interface RedisCmdResponse {
  out?: string
  needConfirm?: boolean
  cmd?: string
  error?: string
}

export interface AuditRow {
  ts: string
  user: string
  connId: number
  action: string
  detail: string
  success: boolean
}

export interface DDLPrefill {
  name?: string
  type?: string
  nullable: boolean
  default?: string
}
