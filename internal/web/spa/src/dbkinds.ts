// Single source of truth for the database types the app can connect to.
//
// The backend has three SQL/NoSQL engines: a PostgreSQL path (pgx + pg_catalog),
// a MySQL/MariaDB path (database/sql + information_schema), and a special-cased
// Redis path. Each handler dispatches on the engine family; any database that
// speaks the PostgreSQL wire protocol (CockroachDB, Greenplum, Redshift, …) or
// the MySQL wire protocol (MariaDB) works through the matching engine for free
// it only needs an entry here.
//
// To add a new PG-wire- or MySQL-wire-compatible type: add one row with the right
// engine. A genuinely different SQL/NoSQL engine additionally needs backend
// support (a new internal/* package implementing dbsql.Engine + a registry
// dispatch entry, mirrored by internal/dbsql.kindFamily on the Go side).

export type Engine = 'postgres' | 'mysql' | 'redis'

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
  { id: 'mysql',     label: 'MySQL',               engine: 'mysql',    defaultPort: 3306,  schemes: ['mysql'] },
  { id: 'mariadb',   label: 'MariaDB',             engine: 'mysql',    defaultPort: 3306,  schemes: ['mariadb', 'maria'] },
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
