// Typed client for the verix-dbm JSON API. State-changing calls carry the CSRF
// token in X-CSRF-Token (matching the Go auth.CheckCSRF check). All requests are
// same-origin and rely on the session cookie, so credentials default to include.

import type {
  Me, Connection, ExplorerData, Column, Index, Key, GridResponse, QueryResponse,
  DocResponse, UsagesResponse, RedisKeysResponse, RedisValue, RedisCmdResponse,
  AuditRow, DDLPrefill, Role, Grant, GrantLevel,
} from './types'

let csrf = ''
export function setCSRF(token: string) { csrf = token }

class ApiError extends Error {}

async function req<T>(method: string, url: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET') headers['X-CSRF-Token'] = csrf
  const res = await fetch(url, {
    method,
    headers,
    credentials: 'same-origin',
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await res.text()
  let data: any = null
  if (text) { try { data = JSON.parse(text) } catch { data = { error: text } } }
  if (!res.ok) throw new ApiError(data?.error || `${res.status} ${res.statusText}`)
  return data as T
}

const get = <T>(u: string) => req<T>('GET', u)
const qs = (o: Record<string, string | number | undefined>) => {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== '') p.set(k, String(v))
  const s = p.toString()
  return s ? '?' + s : ''
}

export type ConnInput = {
  name: string; kind: string; host: string; port: number; dbname: string
  username: string; password?: string; copyFrom?: number; options: string; readOnly: boolean
}

export const api = {
  me: () => get<Me>('/api/me'),

  listConnections: () => get<{ connections: Connection[] }>('/api/connections'),
  getConnection: (id: number) => get<{ connection: Connection }>(`/api/connections/${id}`),
  createConnection: (in_: ConnInput) => req<{ id: number }>('POST', '/api/connections', in_),
  updateConnection: (id: number, in_: ConnInput) => req<{ ok: boolean }>('PUT', `/api/connections/${id}`, in_),
  deleteConnection: (id: number) => req<{ ok: boolean }>('DELETE', `/api/connections/${id}`),
  testConnection: (in_: ConnInput) => req<{ ok: boolean; error?: string }>('POST', '/api/connections/test', in_),

  // Re-encrypt every stored credential under the current primary key (admin;
  // the second half of a key rotation).
  reencrypt: () => req<{ primaryKey: string; checked: number; rewritten: number; failed: number }>('POST', '/api/admin/reencrypt'),

  // Per-connection access grants (admin only).
  listGrants: (id: number) => get<{ grants: Grant[] }>(`/api/connections/${id}/grants`),
  setGrant: (id: number, subject: string, level: GrantLevel) =>
    req<{ ok: boolean }>('PUT', `/api/connections/${id}/grants`, { subject, level }),
  deleteGrant: (id: number, gid: number) =>
    req<{ ok: boolean }>('DELETE', `/api/connections/${id}/grants/${gid}`),

  explorer: (id: number) => get<ExplorerData>(`/api/c/${id}/explorer`),
  columns: (id: number, schema: string, table: string) =>
    get<{ columns: Column[] }>(`/api/c/${id}/pg/columns${qs({ schema, table })}`),
  indexes: (id: number, schema: string, table: string) =>
    get<{ indexes: Index[] | null }>(`/api/c/${id}/pg/indexes${qs({ schema, table })}`),
  keys: (id: number, schema: string, table: string) =>
    get<{ keys: Key[] | null }>(`/api/c/${id}/pg/keys${qs({ schema, table })}`),

  grid: (id: number, p: { schema: string; table: string; where?: string; order?: string; page?: number; size?: number }) =>
    get<GridResponse>(`/api/c/${id}/grid${qs(p)}`),
  query: (id: number, sql: string, confirm = false, schema?: string) =>
    req<QueryResponse>('POST', `/api/c/${id}/pg/query`, { sql, confirm, schema }),
  // Commit a batch of write statements as one atomic transaction (grid Tx: Manual).
  execTx: (id: number, statements: string[], confirm = false) =>
    req<{ ok?: boolean; count?: number; needConfirm?: boolean }>('POST', `/api/c/${id}/pg/tx`, { statements, confirm }),
  generate: (id: number, kind: string, schema: string, table: string) =>
    get<{ sql: string }>(`/api/c/${id}/pg/generate${qs({ kind, schema, table })}`),
  doc: (id: number, schema: string, table: string) =>
    get<DocResponse>(`/api/c/${id}/pg/doc${qs({ schema, table })}`),
  usages: (id: number, schema: string, table: string) =>
    get<UsagesResponse>(`/api/c/${id}/pg/usages${qs({ schema, table })}`),
  ddlPrefill: (id: number, kind: string, schema: string, table: string, column: string) =>
    get<DDLPrefill>(`/api/c/${id}/pg/form${qs({ kind, schema, table, column })}`),
  runForm: (id: number, body: Record<string, unknown>) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/ddl/run`, body),
  // Atomically run the DDL a table-designer create/modify produced.
  applyTable: (id: number, action: string, statements: string[]) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/table/apply`, { action, statements }),
  dropTable: (id: number, schema: string, table: string) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/table/drop`, { schema, table }),
  truncate: (id: number, schema: string, table: string) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/table/truncate`, { schema, table }),
  dropColumn: (id: number, schema: string, table: string, column: string) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/column/drop`, { schema, table, column }),
  dropIndex: (id: number, schema: string, table: string, name: string) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/index/drop`, { schema, table, name }),
  dropSchema: (id: number, schema: string, cascade: boolean) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/schema/drop`, { schema, cascade }),
  alterSchema: (id: number, schema: string, newName: string, owner: string) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/schema/alter`, { schema, newName, owner }),

  roles: (id: number) => get<{ roles: Role[] | null }>(`/api/c/${id}/pg/roles`),
  dropRole: (id: number, name: string, host = '') =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/role/drop`, { name, host }),
  alterRole: (id: number, body: Record<string, unknown>) =>
    req<{ ok: boolean }>('POST', `/api/c/${id}/pg/role/alter`, body),

  redisKeys: (id: number, match: string, cursor = 0) =>
    get<RedisKeysResponse>(`/api/c/${id}/redis/keys${qs({ match, cursor })}`),
  redisValue: (id: number, key: string) =>
    get<{ value: RedisValue }>(`/api/c/${id}/redis/value${qs({ key })}`),
  redisCmd: (id: number, cmd: string, confirm = false) =>
    req<RedisCmdResponse>('POST', `/api/c/${id}/redis/cmd`, { cmd, confirm }),

  audit: () => get<{ rows: AuditRow[] }>('/api/audit'),
  // Full audit log download (admin) for SIEM/forensics. jsonl or csv.
  auditExport: async (format: 'jsonl' | 'csv') => {
    const res = await fetch(`/api/audit/export?format=${format}`, { credentials: 'same-origin' })
    if (!res.ok) throw new ApiError(await res.text())
    const blob = await res.blob()
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `audit.${format}`
    a.click()
    URL.revokeObjectURL(a.href)
  },

  // CSV/JSON export is a file download: post the CSRF header, then save the blob.
  exportTable: async (id: number, schema: string, table: string, where: string, order: string, format: string) => {
    const res = await fetch(`/c/${id}/export${qs({ schema, table, where, order, format })}`, {
      headers: { 'X-CSRF-Token': csrf },
      credentials: 'same-origin',
    })
    if (!res.ok) throw new ApiError(await res.text())
    const blob = await res.blob()
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${schema}_${table}.${format}`.replace(/[^a-zA-Z0-9_.-]/g, '_')
    a.click()
    URL.revokeObjectURL(a.href)
  },
}
