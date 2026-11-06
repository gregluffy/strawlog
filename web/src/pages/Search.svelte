<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, shortTs, highlight } from '../lib/fmt.js'
  import { app, go, setNode, nodeName } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import Select from '../lib/ui/Select.svelte'
  import Switch from '../lib/ui/Switch.svelte'

  const sinceOpts = [
    { value: '', label: 'Any time' }, { value: '15m', label: 'Last 15 minutes' }, { value: '1h', label: 'Last hour' },
    { value: '6h', label: 'Last 6 hours' }, { value: '24h', label: 'Last 24 hours' }, { value: '7d', label: 'Last 7 days' },
  ]
  const limitOpts = [100, 500, 1000, 5000].map((k) => ({ value: k, label: `Max ${k.toLocaleString()}` }))

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
    <div class="qrow">
      <div class="qbox">
        <Icon name="search" size={17} />
        <!-- svelte-ignore a11y_autofocus -->
        <input class="q mono" placeholder={regex ? 'Regular expression, e.g. timeout|refused' : 'Text to find, e.g. connection refused'} bind:value={query} autofocus />
        <div class="qflags">
          <button type="button" class="flag" class:on={regex} aria-pressed={regex} title="Regular expression" onclick={() => (regex = !regex)}>.*</button>
          <button type="button" class="flag" class:on={caseSensitive} aria-pressed={caseSensitive} title="Match case" onclick={() => (caseSensitive = !caseSensitive)}>Aa</button>
        </div>
      </div>
      <button class="primary go" disabled={busy}>{#if busy}<span class="spin"></span>{/if} Search</button>
    </div>
    <div class="row opts">
      <Select bind:value={since} options={sinceOpts} icon="clock" size="sm" label="Time range" menuWidth={180} />
      <Select bind:value={limit} options={limitOpts} size="sm" label="Max results" />
      <span class="sep"></span>
      <div class="lvls">
        <span class="dim small">Levels</span>
        {#each Object.keys(lv) as k}<button type="button" class="lvchip {k}" class:on={lv[k]} aria-pressed={lv[k]} onclick={() => (lv[k] = !lv[k])}><span class="ldot"></span>{k}</button>{/each}
      </div>
      <span class="sep"></span>
      <Switch bind:checked={includeRotated} label="Rotated & .gz" size="sm" title="Also scan rotated and compressed files" />
      <Switch bind:checked={allNodes} label="All nodes" size="sm" />
    </div>
    <div class="row scope">
      <span class="muted small">Scope:</span>
      {#if sourceId && !allNodes}
        <span class="badge info">{srcName[sourceId] || sourceId}</span>
        <button type="button" class="ghost sm" onclick={() => { sourceId = ''; go('/search') }}><Icon name="x" size={13} /> All sources</button>
      {:else}
        <button type="button" class="chip" class:on={selApps.length === 0} onclick={() => (selApps = [])}>All apps on {allNodes ? 'all nodes' : nodeName(app.node)}</button>
        {#each apps as a}
          <button type="button" class="chip" class:on={selApps.includes(a)} onclick={() => toggleApp(a)}>{a}</button>
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
      <span class="right"><Switch bind:checked={wrap} label="Wrap" size="sm" /></span>
    </div>
    {#each result.errors as e}<div class="alert warn small" style="margin-bottom:6px">{e}</div>{/each}
    <div class="card tight">
      <div class="logbox results" class:nowrap={!wrap}>
        {#if result.lines.length === 0}<div class="empty"><div class="ei"><Icon name="search" size={22} /></div>Nothing found.</div>{/if}
        {#each result.lines as l, i (i)}
          <div class="ll {l.l || ''}">
            {#if l.t}<span class="ts">{shortTs(l.t)}</span>{/if}
            <button class="src ghost linkish" onclick={() => openSrc(l)} title={(l.f || l.s) + ' on ' + nodeName(l.node)}>{allNodes ? nodeName(l.node) + ' / ' : ''}{l.s.startsWith('journal:') && l.st ? l.st : srcName[l.s] || l.s.replace(/^\w+:/, '')}</button>
            <span class="msg">{#each highlight(l.m, query, regex, caseSensitive) as p}{#if p.m}<mark>{p.t}</mark>{:else}{p.t}{/if}{/each}</span>
          </div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .form { display: flex; flex-direction: column; gap: 12px; }
  .qrow { display: flex; gap: 10px; }
  .qbox { position: relative; flex: 1; display: flex; align-items: center; }
  .qbox > :global(.icon) { position: absolute; left: 13px; color: var(--dim); pointer-events: none; }
  .q { width: 100%; height: 42px; font-size: 14px; padding: 0 92px 0 40px; border-radius: var(--r-md); }
  .go { height: 42px; padding: 0 20px; border-radius: var(--r-md); }
  .qflags { position: absolute; right: 6px; display: flex; gap: 3px; }
  .flag { height: 28px; min-width: 34px; padding: 0 8px; font-family: var(--mono); font-size: 12.5px; box-shadow: none; background: transparent; border-color: transparent; color: var(--dim); }
  .flag.on { background: var(--accent-soft); color: var(--accent); border-color: color-mix(in srgb, var(--accent) 40%, transparent); }
  .opts { gap: 12px; font-size: 13px; }
  .lvls { display: inline-flex; gap: 4px; align-items: center; }
  .lvls .small { margin-right: 4px; }
  .lvchip { height: 26px; padding: 0 9px; border-radius: 99px; font-size: 12px; gap: 6px; box-shadow: none; background: transparent; color: var(--dim); text-transform: capitalize; }
  .lvchip .ldot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; opacity: .5; }
  .lvchip.on { background: var(--field); color: var(--text); border-color: var(--border-strong); }
  .lvchip.error .ldot { background: var(--err); } .lvchip.warn .ldot { background: var(--warn); } .lvchip.info .ldot { background: var(--info); } .lvchip.debug .ldot { background: var(--debug); }
  .lvchip.on .ldot { opacity: 1; }
  .scope { gap: 6px; }
  .chip { border-radius: 99px; }
  .chip.on { background: var(--accent); color: var(--accent-ink); border-color: var(--accent); }
  .small { font-size: 12px; }
  .hint { margin: 0; }
  .meta { margin: 16px 0 8px; gap: 14px; }
  .results { max-height: calc(100vh - 330px); min-height: 200px; }
  .linkish { padding: 0; border: 0; height: auto; font: inherit; color: var(--info); background: none; cursor: pointer; box-shadow: none; display: inline; text-align: left; width: 180px; overflow: hidden; text-overflow: ellipsis; }
  .linkish:hover:not(:disabled) { background: none; text-decoration: underline; }
</style>
