<script>
  import { onMount, tick } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, shortTs, highlight } from '../lib/fmt.js'
  import { app, go, save, nodeName } from '../lib/state.svelte.js'

  const MAX = 4000
  let sources = $state([])
  let sourceId = $state(app.route.params.source || '')
  let unit = $state(app.route.params.unit || '')
  let lines = $state([])
  let n = $state(500)
  let follow = $state(false)
  let wrap = $state(true)
  let filter = $state('')
  let levels = $state({ error: true, warn: true, info: true, debug: true, '': true })
  let pickQ = $state('')
  let loading = $state(false)
  let error = $state('')
  let box
  let stick = true
  let es = null

  let source = $derived(sources.find((s) => s.id === sourceId))
  let shown = $derived.by(() => {
    const f = filter.toLowerCase()
    return lines.filter((l) => levels[l.l || ''] !== false && (!f || l.m.toLowerCase().includes(f)))
  })
  let pick = $derived.by(() => {
    const ql = pickQ.toLowerCase()
    const m = new Map()
    for (const s of sources) {
      if (ql && !(s.name + ' ' + s.app).toLowerCase().includes(ql)) continue
      if (!m.has(s.app)) m.set(s.app, [])
      m.get(s.app).push(s)
    }
    return [...m.entries()]
  })

  async function loadSources() {
    try { sources = await api.get(api.n(app.node, '/sources')) } catch (e) { error = e.message }
    if (!sourceId && sources.length) {
      const d = sources.find((s) => s.kind === 'docker' && s.state === 'running') || sources[0]
      select(d.id)
    } else if (sourceId) load()
  }

  function stop() {
    if (es) { es.close(); es = null }
  }

  async function load() {
    stop()
    if (!sourceId) return
    error = ''
    loading = true
    lines = []
    if (follow) {
      startFollow()
      loading = false
      return
    }
    try {
      lines = await api.post(api.n(app.node, '/tail'), { source: sourceId, lines: Number(n), unit })
    } catch (e) { error = e.message }
    loading = false
    stick = true
    scrollEnd()
  }

  function startFollow() {
    const qs = new URLSearchParams({ source: sourceId, lines: String(Math.min(Number(n), 1000)), unit })
    es = new EventSource(api.n(app.node, '/follow?' + qs))
    let pending = []
    let raf = 0
    es.onmessage = (ev) => {
      try { pending.push(JSON.parse(ev.data)) } catch {}
      if (!raf) raf = requestAnimationFrame(() => {
        raf = 0
        let next = lines.concat(pending)
        pending = []
        if (next.length > MAX) next = next.slice(next.length - MAX)
        lines = next
        scrollEnd()
      })
    }
    es.addEventListener('error', (ev) => {
      if (ev.data) { try { error = JSON.parse(ev.data).error } catch {} }
    })
    es.addEventListener('end', () => stop())
  }

  async function scrollEnd() {
    await tick()
    if (box && stick) box.scrollTop = box.scrollHeight
  }
  function onScroll() {
    stick = box.scrollHeight - box.scrollTop - box.clientHeight < 40
  }

  function select(id) {
    sourceId = id
    unit = ''
    go('/logs', { source: id })
    load()
  }

  function toggleFollow() {
    follow = !follow
    load()
  }

  onMount(() => {
    try { wrap = localStorage.getItem('strawlog.wrap') !== '0' } catch {}
    loadSources()
    return stop
  })
  let lastNode = app.node
  $effect(() => { if (app.node !== lastNode) { lastNode = app.node; stop(); sourceId = ''; lines = []; loadSources() } })
  $effect(() => save('strawlog.wrap', wrap ? '1' : '0'))

  function dl() {
    const qs = new URLSearchParams({ source: sourceId, unit })
    location.href = api.n(app.node, '/download?' + qs)
  }
  function copy() {
    navigator.clipboard?.writeText(shown.map((l) => (l.t ? l.t + ' ' : '') + l.m).join('\n'))
  }
</script>

<div class="logs">
  <div class="picker">
    <input placeholder="Find source…" bind:value={pickQ} />
    <div class="plist">
      {#each pick as [appName, items]}
        <div class="papp">{appName}</div>
        {#each items as s (s.id)}
          <button class="pitem ghost" class:active={s.id === sourceId} onclick={() => select(s.id)} title={s.path}>
            <span class="badge kind-{s.kind} tiny">{s.kind[0].toUpperCase()}</span>
            <span class="trunc grow">{s.name}</span>
            <span class="dim sz">{bytes(s.totalSize)}</span>
          </button>
        {/each}
      {/each}
    </div>
  </div>

  <div class="viewer">
    <div class="toolbar">
      <div class="title trunc">
        <b>{source?.name || 'Pick a source'}</b>
        <span class="dim">{nodeName(app.node)}{source ? ' · ' + source.app : ''}</span>
      </div>
      {#if source?.kind === 'journal'}
        <select bind:value={unit} onchange={load} style="max-width:200px">
          <option value="">All units</option>
          {#each source.units || [] as u}<option value={u}>{u}</option>{/each}
        </select>
      {/if}
      <select bind:value={n} onchange={load} title="Lines">
        {#each [100, 500, 1000, 2000, 5000] as k}<option value={k}>{k} lines</option>{/each}
      </select>
      <button class:primary={follow} onclick={toggleFollow} disabled={!sourceId}>{follow ? '● Live' : '▶ Follow'}</button>
      <button onclick={load} disabled={!sourceId || loading}>{#if loading}<span class="spin"></span>{/if} Reload</button>
    </div>
    <div class="toolbar sub">
      <input placeholder="Filter shown lines…" bind:value={filter} class="grow" />
      {#each ['error', 'warn', 'info', 'debug'] as lv}
        <label class="check lv {lv}"><input type="checkbox" bind:checked={levels[lv]} /> {lv}</label>
      {/each}
      <label class="check"><input type="checkbox" bind:checked={wrap} /> wrap</label>
      <button class="sm" onclick={copy} title="Copy shown lines">Copy</button>
      <button class="sm" onclick={dl} disabled={!sourceId} title="Download last 5000 lines">Download</button>
      <button class="sm" onclick={() => go('/search', { source: sourceId })} disabled={!sourceId}>Search all…</button>
    </div>
    {#if error}<div class="alert err" style="margin:8px 12px">{error}</div>{/if}
    <div class="logbox" class:nowrap={!wrap} bind:this={box} onscroll={onScroll}>
      {#if !loading && sourceId && shown.length === 0}
        <div class="empty">{lines.length ? 'No lines match the filter.' : follow ? 'Waiting for new lines…' : 'No log lines.'}</div>
      {/if}
      {#each shown as l, i (i)}
        <div class="ll {l.l || ''} {l.st === 'stderr' ? 'stderr' : ''}">
          {#if l.t}<span class="ts">{shortTs(l.t)}</span>{/if}
          {#if source?.kind === 'journal' && l.st}<span class="src">{l.st}</span>{/if}
          <span class="msg">{#each highlight(l.m, filter, false, false) as p}{#if p.m}<mark>{p.t}</mark>{:else}{p.t}{/if}{/each}</span>
        </div>
      {/each}
    </div>
    <div class="status dim">
      {shown.length} / {lines.length} lines{follow ? ' · live' : ''}{source ? ' · ' + bytes(source.totalSize) + ' on disk' : ''}
      {#if source?.path}<span class="mono right trunc">{source.path}</span>{/if}
    </div>
  </div>
</div>

<style>
  .logs { display: grid; grid-template-columns: 260px 1fr; height: 100vh; }
  .picker { border-right: 1px solid var(--border); display: flex; flex-direction: column; min-height: 0; background: var(--bg); }
  .picker input { margin: 12px; }
  .plist { overflow-y: auto; padding: 0 6px 12px; }
  .papp { font-size: 11px; text-transform: uppercase; letter-spacing: .05em; color: var(--dim); padding: 10px 8px 3px; }
  .pitem { width: 100%; padding: 4px 8px; text-align: left; gap: 8px; font-size: 13px; }
  .pitem.active { background: var(--panel); box-shadow: inset 2px 0 0 var(--accent); }
  .tiny { padding: 0 5px; font-size: 10px; }
  .sz { font-size: 11px; }
  .viewer { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
  .toolbar { display: flex; gap: 8px; align-items: center; padding: 10px 12px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
  .toolbar.sub { padding: 7px 12px; background: var(--bg2); font-size: 12.5px; }
  .title { flex: 1; display: flex; gap: 10px; align-items: baseline; min-width: 160px; padding-right: 40px; }
  .lv.error { color: var(--err); } .lv.warn { color: var(--warn); } .lv.info { color: var(--info); } .lv.debug { color: var(--debug); }
  .logbox { flex: 1; min-height: 0; padding: 6px 0; }
  .status { display: flex; gap: 12px; font-size: 11.5px; padding: 5px 12px; border-top: 1px solid var(--border); }
  .status .right { max-width: 50%; }
  @media (max-width: 860px) { .logs { grid-template-columns: 1fr; grid-template-rows: 180px 1fr; } .picker { border-right: 0; border-bottom: 1px solid var(--border); } }
</style>
