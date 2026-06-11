import { useEffect, useRef, useState } from 'react'
import { api } from '../../api'
import { useApp } from '../../appctx'
import type { RedisCmdResponse, RedisKeyInfo, RedisValue } from '../../types'

// Redis/Valkey tab: a keyspace browser (SCAN with MATCH + cursor paging), a
// type-aware value viewer, and a command console with a confirm gate for
// dangerous commands. Mirrors "redisTab" + its sub-partials.
export default function RedisTab({ connId }: { connId: number }) {
  const app = useApp()
  const conn = app.connById(connId)
  const readOnly = conn ? conn.readOnly || !app.caps.write : false

  const [match, setMatch] = useState('*')
  const [keys, setKeys] = useState<RedisKeyInfo[]>([])
  const [cursor, setCursor] = useState(0)
  const [keysErr, setKeysErr] = useState('')
  const [value, setValue] = useState<RedisValue | null>(null)
  const [valueErr, setValueErr] = useState('')

  const [cmd, setCmd] = useState('')
  const [cmdResp, setCmdResp] = useState<RedisCmdResponse | null>(null)
  const debounce = useRef<number | undefined>(undefined)

  const loadKeys = (m: string, cur: number, append: boolean) => {
    api.redisKeys(connId, m, cur).then(r => {
      setKeys(prev => append ? [...prev, ...(r.keys || [])] : (r.keys || []))
      setCursor(r.cursor)
      setKeysErr('')
    }).catch(e => setKeysErr(String(e.message || e)))
  }

  useEffect(() => { loadKeys('*', 0, false) }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const onMatch = (m: string) => {
    setMatch(m)
    window.clearTimeout(debounce.current)
    debounce.current = window.setTimeout(() => loadKeys(m, 0, false), 300)
  }

  const openKey = (key: string) => {
    api.redisValue(connId, key).then(r => { setValue(r.value); setValueErr('') })
      .catch(e => { setValue(null); setValueErr(String(e.message || e)) })
  }

  const runCmd = (confirm = false) => {
    api.redisCmd(connId, cmd, confirm).then(setCmdResp).catch(e => setCmdResp({ error: String(e.message || e) }))
  }

  return (
    <div className="redis-pane">
      <aside className="redis-keys hud-panel">
        <h3 className="hud-label">Keyspace</h3>
        <input className="hud-input" type="search" value={match} onChange={e => onMatch(e.target.value)} placeholder="match e.g. seal:*" />
        <div>
          {keysErr && <div className="alert error">{keysErr}</div>}
          <ul className="keylist">
            {keys.length === 0 && !keysErr ? <li className="dim">no keys match</li>
              : keys.map((k, i) => (
                <li key={i}>
                  <a style={{ cursor: 'pointer' }} onClick={() => openKey(k.Key)}>{k.Key}</a>
                  <span className="kt">{k.Type}</span><span className="dim ttl">{k.TTL}</span>
                </li>
              ))}
            {cursor !== 0 && <li><button className="hud-btn-accent sm" onClick={() => loadKeys(match, cursor, true)}>more…</button></li>}
          </ul>
        </div>
      </aside>

      <div className="redis-main">
        <div className="hud-panel p-4 panel-grow">
          <h3 className="hud-heading">Value</h3>
          <div className="grow-body">
            {valueErr && <div className="alert error code">{valueErr}</div>}
            {!value && !valueErr && <p className="dim">Select a key from the keyspace to inspect it.</p>}
            {value && <RedisValueView v={value} />}
          </div>
        </div>

        <div className="hud-panel p-4">
          <h3 className="hud-heading">Command console {readOnly && <span className="ro">READ-ONLY</span>}</h3>
          <form className="stack" onSubmit={e => { e.preventDefault(); runCmd(false) }}>
            <input className="hud-input code" value={cmd} onChange={e => setCmd(e.target.value)}
              placeholder="GET seal:session:abc   ·   SCAN 0 MATCH ch:*" />
            <div className="row end"><button className="hud-btn-cta" type="submit">Run</button></div>
          </form>
          <div>
            {cmdResp?.error && <div className="alert error code">{cmdResp.error}</div>}
            {cmdResp?.needConfirm && (
              <div className="alert warn">
                <p className="hud-label">Dangerous command. Confirm to run:</p>
                <pre className="code">{cmdResp.cmd}</pre>
                <button className="btn-danger" type="button" onClick={() => runCmd(true)}>Yes, run it</button>
              </div>
            )}
            {cmdResp?.out !== undefined && cmdResp.out !== null && <pre className="code val">{cmdResp.out}</pre>}
          </div>
        </div>
      </div>
    </div>
  )
}

function RedisValueView({ v }: { v: RedisValue }) {
  const meta = <div className="vmeta hud-label">{v.Type} · TTL {v.TTL} · <span className="dim">{v.Key}</span></div>
  if (v.Type === 'hash' || v.Type === 'zset') {
    return (
      <>
        {meta}
        <div className="tablewrap"><table className="data">
          <thead><tr><th>{v.Type === 'zset' ? 'member' : 'field'}</th><th>{v.Type === 'zset' ? 'score' : 'value'}</th></tr></thead>
          <tbody>{(v.Pairs || []).map((p, i) => <tr key={i}><td className="code">{p[0]}</td><td className="code">{p[1]}</td></tr>)}</tbody>
        </table></div>
      </>
    )
  }
  if (v.Type === 'list' || v.Type === 'set') {
    return <>{meta}<ol className="vlist code">{(v.List || []).map((x, i) => <li key={i}>{x}</li>)}</ol></>
  }
  return <>{meta}<pre className="code val">{v.Text}</pre></>
}
