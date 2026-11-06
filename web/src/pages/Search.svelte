<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, shortTs, highlight } from '../lib/fmt.js'
  import { app, go, setNode, nodeName } from '../lib/state.svelte.js'

  let sources = $state([])
  let query = $state('')
  let regex = $state(false)
  let caseSensitive = $state(false)
  let includeRotated = $state(false)
  let since = $state('')
  let limit = $state(500)
  let allNodes = $state(false)
  let selApps = $state([])
  let sourceId = $state(app.route.params.source || '')
  let lv = $state({ error: false, warn: false, info: false, debug: false })
  let result = $state(null)
  let busy = $state(false)
  let error = $state('')
  let wrap = $state(true)

  let apps = $derived([...new Set(sources.map((s) => s.app))].sort())
  let srcName = $derived(Object.fromEntries(sources.map((s) => [s.id, s.name])))

  onMount(async () => {
    try { sources = await api.get(api.n(app.node, '/sources')) } catch {}
  })

  async function run(e) {
    e?.preventDefault()
    busy = true
    error = ''
    const levels = Object.keys(lv).filter((k) => lv[k])
    const body = {
      nodes: allNodes ? [] : [app.node],
      query, regex, caseSensitive, includeRotated, since, limit: Number(limit), levels,
      apps: sourceId ? [] : selApps,
      sources: sourceId && !allNodes ? [sourceId] : [],
    }
    const t = performance.now()
    try {
      const res = await api.post('/api/search', body)
      const lines = []
      let matches = 0, scanned = 0, truncated = false
      const errors = []
      for (const [node, r] of Object.entries(res)) {
        if (r.error) errors.push(nodeName(node) + ': ' + r.error)
        for (const e of r.errors || []) errors.push(nodeName(node) + ': ' + e)
        matches += r.matches || 0
        scanned += r.scannedBytes || 0
        truncated ||= r.truncated
        for (const l of r.lines) lines.push({ ...l, node })
      }
      lines.sort((a, b) => (a.t && b.t ? (a.t < b.t ? -1 : a.t > b.t ? 1 : 0) : 0))
      result = { lines, matches, scanned, truncated, errors, ms: Math.round(performance.now() - t) }
    } catch (err) { error = err.message }
    busy = false
  }

  function toggleApp(a) {
    selApps = selApps.includes(a) ? selApps.filter((x) => x !== a) : [...selApps, a]
  }
  function openSrc(l) { setNode(l.node); go('/logs', { source: l.s }) }
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Search</h1><p class="sub">Scans the logs where they are (no index, no extra disk use). Docker JSON files are read directly when mounted.</p></div>
  </div>

  <form class="card form" onsubmit={run}>
    <div class="row">
      <!-- svelte-ignore a11y_autofocus -->
      <input class="grow q mono" placeholder={regex ? 'regular expression, e.g. timeout|refused' : 'text to find, e.g. connection refused'} bind:value={query} autofocus />
      <button class="primary" disabled={busy}>{#if busy}<span class="spin"></span>{/if} Search</button>
    </div>
    <div class="row opts">
      <label class="check"><input type="checkbox" bind:checked={regex} /> regex</label>
      <label class="check"><input type="checkbox" bind:checked={caseSensitive} /> case sensitive</label>
      <label class="check" title="Also scan rotated and .gz files"><input type="checkbox" bind:checked={includeRotated} /> include rotated/.gz</label>
      <label class="check"><input type="checkbox" bind:checked={allNodes} /> all nodes</label>
      <span class="sep"></span>
      {#each Object.keys(lv) as k}<label class="check lv {k}"><input type="checkbox" bind:checked={lv[k]} /> {k}</label>{/each}
      <span class="sep"></span>
      <label class="check">since
        <select bind:value={since}>
          <option value="">any time</option><option value="15m">15 min</option><option value="1h">1 hour</option><option value="6h">6 hours</option><option value="24h">24 hours</option><option value="7d">7 days</option>
        </select>
      </label>
      <label class="check">max <select bind:value={limit}>{#each [100, 500, 1000, 5000] as k}<option value={k}>{k}</option>{/each}</select></label>
    </div>
    <div class="row scope">
      <span class="muted small">Scope:</span>
      {#if sourceId && !allNodes}
        <span class="badge info">{srcName[sourceId] || sourceId}</span>
        <button type="button" class="ghost sm" onclick={() => { sourceId = ''; go('/search') }}>✕ all sources</button>
      {:else}
        {#if selApps.length === 0}<span class="badge">all apps on {allNodes ? 'all nodes' : nodeName(app.node)}</span>{/if}
        {#each apps as a}
          <button type="button" class="chip sm" class:on={selApps.includes(a)} onclick={() => toggleApp(a)}>{a}</button>
        {/each}
      {/if}
    </div>
    <p class="dim small hint">“since” filters Docker and journald by timestamp; plain files have no reliable timestamps, so they are always scanned in full.</p>
  </form>

  {#if error}<div class="alert err" style="margin-top:12px">{error}</div>{/if}

  {#if result}
    <div class="row meta">
      <b>{result.matches.toLocaleString()} matches</b>
      {#if result.truncated}<span class="badge warn">showing newest {result.lines.length}</span>{/if}
      <span class="muted">scanned {bytes(result.scanned)} in {result.ms} ms</span>
      <label class="check right small"><input type="checkbox" bind:checked={wrap} /> wrap</label>
    </div>
    {#each result.errors as e}<div class="alert warn small" style="margin-bottom:6px">{e}</div>{/each}
    <div class="card tight">
      <div class="logbox results" class:nowrap={!wrap}>
        {#if result.lines.length === 0}<div class="empty">Nothing found.</div>{/if}
        {#each result.lines as l, i (i)}
          <div class="ll {l.l || ''}">
            {#if l.t}<span class="ts">{shortTs(l.t)}</span>{/if}
            <button class="src ghost linkish" onclick={() => openSrc(l)} title={(l.f || l.s) + ' on ' + nodeName(l.node)}>{allNodes ? nodeName(l.node) + '/' : ''}{srcName[l.s] || l.s.replace(/^\w+:/, '')}</button>
            <span class="msg">{#each highlight(l.m, query, regex, caseSensitive) as p}{#if p.m}<mark>{p.t}</mark>{:else}{p.t}{/if}{/each}</span>
          </div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .form { display: flex; flex-direction: column; gap: 12px; }
  .q { font-size: 14px; padding: 8px 12px; }
  .opts { gap: 14px; font-size: 13px; }
  .sep { width: 1px; height: 18px; background: var(--border); }
  .lv.error { color: var(--err); } .lv.warn { color: var(--warn); } .lv.info { color: var(--info); } .lv.debug { color: var(--debug); }
  .scope { gap: 6px; }
  .chip { border-radius: 99px; }
  .chip.on { background: var(--accent); color: var(--accent-ink); border-color: var(--accent); }
  .small { font-size: 12px; }
  .hint { margin: 0; }
  .meta { margin: 16px 0 8px; gap: 14px; }
  .results { max-height: calc(100vh - 330px); min-height: 200px; }
  .linkish { padding: 0; border: 0; font: inherit; color: var(--info); background: none; cursor: pointer; }
</style>
