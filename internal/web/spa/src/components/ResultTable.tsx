import type { QueryResult } from '../types'

// Mirrors the "resultTable" partial: a SELECT grid, or a command summary line.
export default function ResultTable({ r }: { r: QueryResult }) {
  if (!r.isSelect) {
    return <p className="ok hud-label">{r.command} · {r.rowsAffected} rows affected · {r.duration}</p>
  }
  const rows = r.rows || []
  if (rows.length === 0) return <p className="dim">0 rows · {r.duration}</p>
  return (
    <>
      <div className="tablewrap">
        <table className="data">
          <thead><tr>{(r.columns || []).map((c, i) => <th key={i}>{c}</th>)}</tr></thead>
          <tbody>
            {rows.map((row, i) => <tr key={i}>{row.map((v, j) => <td key={j} className="code">{v}</td>)}</tr>)}
          </tbody>
        </table>
      </div>
      <p className="hud-label dim">{rows.length} rows{r.truncated ? ' · truncated at 1000' : ''} · {r.duration}</p>
    </>
  )
}
