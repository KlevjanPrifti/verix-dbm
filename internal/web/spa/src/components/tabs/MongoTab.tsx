import { useCallback, useEffect, useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { MongoCmdResponse, MongoIndex } from '../../types'
import { Ico, RotateCw, ChevronLeft, ChevronRight, Copy, Plus } from '../../icons'

// MongoDB tab: a document browser for one collection (JSON filter / sort /
// projection with skip-based paging) plus a read-only-aware command console with
// a confirm gate for destructive commands. The document-store analog of GridTab
// + RedisTab; MongoDB is not a dbsql.Engine, so this talks to the dedicated
// /api/c/{id}/mongo/* endpoints.
export default function MongoTab({ connId, db, coll }: { connId: number; db: string; coll: string }) {
  const app = useApp()
  const conn = app.connById(connId)
  const readOnly = conn ? conn.readOnly || !app.caps.write : false

  const [filter, setFilter] = useState('')
  const [sort, setSort] = useState('')
  const [projection, setProjection] = useState('')
  const [page, setPage] = useState(0)
  const [size] = useState(50)
  const [docs, setDocs] = useState<string[]>([])
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  const [indexes, setIndexes] = useState<MongoIndex[] | null>(null)
  const [cmd, setCmd] = useState(`{ "find": "${coll}", "limit": 5 }`)
  const [cmdResp, setCmdResp] = useState<MongoCmdResponse | null>(null)

  // Inline "insert document" editor (the document-store analog of GridTab's add
  // row). The body is sent verbatim inside an insert command so MongoDB extended
  // JSON (ObjectId(...), { "$date": … }) is parsed server-side, not by JSON.parse.
  const [insertOpen, setInsertOpen] = useState(false)
  const [insertText, setInsertText] = useState('{\n  \n}')
  const [insertErr, setInsertErr] = useState('')
  const [inserting, setInserting] = useState(false)
  const insertDoc = async () => {
    const body = insertText.trim()
    if (!body) return
    setInserting(true)
    setInsertErr('')
    const command = `{ "insert": ${JSON.stringify(coll)}, "documents": [ ${body} ] }`
    try {
      const resp = await api.mongoCmd(connId, db, command)
      if (resp.error) { setInsertErr(resp.error); return }
      // A successful command can still report per-document writeErrors (e.g. a
      // duplicate _id); surface those instead of claiming success.
      if (resp.out && /writeErrors/.test(resp.out)) { setInsertErr(resp.out); return }
      setInsertOpen(false)
      setInsertText('{\n  \n}')
      app.notify('document inserted')
      load(page)
    } catch (e) {
      setInsertErr(String((e as Error).message || e))
    } finally {
      setInserting(false)
    }
  }

  const load = useCallback((p: number) => {
    setLoading(true)
    api.mongoDocs(connId, { db, coll, filter, sort, projection, page: p, size })
      .then(r => { setDocs(r.docs || []); setHasMore(r.hasMore); setErr('') })
      .catch(e => { setDocs([]); setErr(String(e.message || e)) })
      .finally(() => setLoading(false))
  }, [connId, db, coll, filter, sort, projection, size])

  // Initial load + reload on page change. (load is excluded from deps so editing
  // a filter doesn't refetch on every keystroke; "apply" / pager drive reloads.)
  useEffect(() => { load(page) }, [page]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    api.mongoIndexes(connId, db, coll).then(r => setIndexes(r.indexes || [])).catch(() => setIndexes([]))
  }, [connId, db, coll])

  const apply = (e: React.FormEvent) => { e.preventDefault(); if (page === 0) load(0); else setPage(0) }
  const runCmd = (confirm = false) =>
    api.mongoCmd(connId, db, cmd, confirm).then(setCmdResp).catch(e => setCmdResp({ error: String(e.message || e) }))

  const first = docs.length ? page * size + 1 : 0
  const last = page * size + docs.length

  return (
    <div className="grid-pane">
      <div className="grid-toolbar">
        <button className="tb-ico" title="refresh" onClick={() => load(page)}><RotateCw size={16} /></button>
        {!readOnly && <button className="tb-ico" title="insert document" onClick={() => setInsertOpen(o => !o)}><Plus size={16} /></button>}
        <span className="tb-sep" />
        <span className="tb-chip hud-label">{db}.{coll}</span>
        {indexes && <span className="tb-chip hud-label dim" title={indexes.map(i => `${i.Name}: ${i.Keys}`).join('\n')}>{indexes.length} index{indexes.length === 1 ? '' : 'es'}</span>}
        <span className="tb-grow" />
        {readOnly && <span className="ro">READ-ONLY</span>}
        {conn && <span className="tb-chip conn-chip hud-label" title={`${conn.kind}@${conn.host}`}>{conn.kind}@{conn.host}</span>}
      </div>

      {insertOpen && !readOnly && (
        <div className="mongo-insert hud-panel stack">
          <span className="hud-label dim">New document in <span className="code">{db}.{coll}</span> (JSON / extended JSON)</span>
          <textarea className="hud-input code" rows={5} value={insertText} onChange={e => setInsertText(e.target.value)}
            placeholder={`{ "name": "example", "createdAt": { "$date": "2025-01-01T00:00:00Z" } }`} />
          {insertErr && <div className="alert error code">{insertErr}</div>}
          <div className="row end">
            <button className="hud-btn-accent sm" type="button" onClick={() => { setInsertOpen(false); setInsertErr('') }}>Cancel</button>
            <button className="hud-btn-cta" type="button" disabled={inserting} onClick={insertDoc}>{inserting ? 'Inserting…' : 'Insert'}</button>
          </div>
        </div>
      )}

      <form className="filter-bar" onSubmit={apply}>
        <span className="fb-key hud-label">FILTER</span>
        <input className="fb-input code" value={filter} onChange={e => setFilter(e.target.value)} placeholder={`{ "status": "active" }`} />
        <span className="fb-key hud-label">SORT</span>
        <input className="fb-input code" value={sort} onChange={e => setSort(e.target.value)} placeholder={`{ "createdAt": -1 }`} />
        <span className="fb-key hud-label">FIELDS</span>
        <input className="fb-input code" value={projection} onChange={e => setProjection(e.target.value)} placeholder={`{ "_id": 0, "name": 1 }`} />
        <button className="hud-btn-accent sm" type="submit">apply</button>
      </form>

      <div className="grid-body mongo-docs">
        {err ? <div className="alert error code">{err}</div>
          : loading && docs.length === 0 ? <p className="dim">loading…</p>
          : docs.length === 0 ? <p className="dim">0 documents</p>
          : docs.map((d, i) => (
            <div className="mongo-doc hud-panel" key={page * size + i}>
              <button className="tb-ico mongo-doc-copy" title="copy document" onClick={() => { app.copy(d); app.notify('document copied') }}><Copy size={14} /></button>
              <pre className="code json-view" dangerouslySetInnerHTML={{ __html: highlightJson(d) }} />
            </div>
          ))}
      </div>

      <div className="grid-footer hud-label">
        {conn && <Ico name={conn.kind} className="gf-ico" />}
        <span className="gf-loc">{db}.{coll}</span>
        <span className="tb-grow" />
        <span className="pg-range">{first === last ? `${last}` : `${first}-${last}`}{hasMore ? '+' : ''}</span>
        {page > 0 && <a className="pg-btn" onClick={() => setPage(p => p - 1)}><ChevronLeft size={14} /> prev</a>}
        <span className="gf-page">page {page + 1}</span>
        {hasMore && <a className="pg-btn" onClick={() => setPage(p => p + 1)}>next <ChevronRight size={14} /></a>}
      </div>

      <details className="mongo-console hud-panel">
        <summary className="hud-heading">Command console {readOnly && <span className="ro">READ-ONLY</span>}</summary>
        <p className="hud-label dim">Runs against <span className="code">{db}</span> as a JSON command document, e.g. <span className="code">{`{ "aggregate": "${coll}", "pipeline": [], "cursor": {} }`}</span></p>
        <form className="stack" onSubmit={e => { e.preventDefault(); runCmd(false) }}>
          <textarea className="hud-input code mongo-cmd-input" rows={3} value={cmd} onChange={e => setCmd(e.target.value)} />
          <div className="row end"><button className="hud-btn-cta" type="submit">Run</button></div>
        </form>
        {cmdResp?.error && <div className="alert error code">{cmdResp.error}</div>}
        {cmdResp?.needConfirm && (
          <div className="alert warn">
            <p className="hud-label">Destructive command. Confirm to run:</p>
            <pre className="code">{cmdResp.cmd}</pre>
            <button className="btn-danger" type="button" onClick={() => runCmd(true)}>Yes, run it</button>
          </div>
        )}
        {cmdResp?.out !== undefined && cmdResp.out !== null && (
          <pre className="code val json-view" dangerouslySetInnerHTML={{ __html: highlightJson(cmdResp.out) }} />
        )}
      </details>
    </div>
  )
}

// highlightJson wraps tokens of a pretty-printed JSON string in coloured spans
// (reusing the .j-* classes from hud.css). Input is HTML-escaped first, so the
// result is safe to inject. Mirrors the helper in GridTab.
function highlightJson(json: string): string {
  const esc = json.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  return esc.replace(
    /("(\\u[a-zA-Z0-9]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false)\b|\bnull\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
    m => {
      let cls = 'j-num'
      if (/^"/.test(m)) cls = /:$/.test(m) ? 'j-key' : 'j-str'
      else if (m === 'true' || m === 'false') cls = 'j-bool'
      else if (m === 'null') cls = 'j-null'
      return `<span class="${cls}">${m}</span>`
    })
}
