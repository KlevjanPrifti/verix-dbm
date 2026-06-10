// Single source of truth for the database types the app can connect to.
//
// The backend has two engines: a PostgreSQL path (pgx + pg_catalog) and a
// special-cased Redis path. Every handler dispatches `kind == "redis"` → Redis,
// everything else → PostgreSQL. So any database that speaks the PostgreSQL wire
// protocol (CockroachDB, Greenplum, Redshift, …) works through the existing
// engine for free it only needs an entry here.
//
// To add a new PG-wire-compatible type: add one row with engine 'postgres'.
// A genuinely different SQL/NoSQL engine additionally needs backend support
// (a new introspection package + handler dispatch) see the README/notes.

export type Engine = 'postgres' | 'redis'

export interface DbKind {
  id: string          // value stored in Connection.kind
  label: string       // shown in the driver dropdown / new-source menu
  engine: Engine      // which backend path serves this kind
  defaultPort: number // pre-filled in the connection form
  schemes: string[]   // URL protocol aliases that resolve to this kind
}

// Order here is the order shown in the pickers.
export const DB_KINDS: DbKind[] = [
  { id: 'postgres',  label: 'PostgreSQL',          engine: 'postgres', defaultPort: 5432,  schemes: ['postgresql', 'postgres', 'pg'] },
  { id: 'cockroach', label: 'CockroachDB',         engine: 'postgres', defaultPort: 26257, schemes: ['cockroachdb', 'cockroach', 'crdb'] },
  { id: 'greenplum', label: 'Greenplum',           engine: 'postgres', defaultPort: 5432,  schemes: ['greenplum', 'gpdb'] },
  { id: 'redshift',  label: 'Amazon Redshift',     engine: 'postgres', defaultPort: 5439,  schemes: ['redshift'] },
  { id: 'yugabyte',  label: 'YugabyteDB',          engine: 'postgres', defaultPort: 5433,  schemes: ['yugabytedb', 'yugabyte', 'ysql'] },
  { id: 'timescale', label: 'TimescaleDB',         engine: 'postgres', defaultPort: 5432,  schemes: ['timescaledb', 'timescale'] },
  { id: 'aurorapg',  label: 'Aurora / RDS Postgres', engine: 'postgres', defaultPort: 5432, schemes: ['aurorapg'] },
  { id: 'redis',     label: 'Redis / Valkey',      engine: 'redis',    defaultPort: 6379,  schemes: ['redis', 'rediss', 'valkey'] },
]

export const DEFAULT_KIND = 'postgres'

const BY_ID = new Map(DB_KINDS.map(k => [k.id, k]))
const BY_SCHEME = new Map(DB_KINDS.flatMap(k => k.schemes.map(s => [s, k] as const)))

export const dbKind = (id: string): DbKind | undefined => BY_ID.get(id)
export const dbKindByScheme = (scheme: string): DbKind | undefined => BY_SCHEME.get(scheme.toLowerCase())
export const defaultPort = (id: string): number => BY_ID.get(id)?.defaultPort ?? 5432
export const kindLabel = (id: string): string => BY_ID.get(id)?.label ?? id
export const kindEngine = (id: string): Engine => BY_ID.get(id)?.engine ?? 'postgres'
