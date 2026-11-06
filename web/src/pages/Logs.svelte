<script>
  import { onMount, tick } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, shortTs, highlight } from '../lib/fmt.js'
  import { app, go, save, nodeName } from '../lib/state.svelte.js'
  import Icon from '../lib/ui/Icon.svelte'
  import Select from '../lib/ui/Select.svelte'
  import Switch from '../lib/ui/Switch.svelte'
  import { toast } from '../lib/ui/overlay.svelte.js'

  const lineOpts = [100, 500, 1000, 2000, 5000].map((k) => ({ value: k, label: `${k.toLocaleString()} lines` }))
  const levelDefs = [['error', 'Error'], ['warn', 'Warn'], ['info', 'Info'], ['debug', 'Debug']]

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
  async function copy() {
    try {
      await navigator.clipboard.writeText(shown.map((l) => (l.t ? l.t + ' ' : '') + l.m).join('\n'))
      toast(`Copied ${shown.length} lines`)
    } catch { toast('Clipboard not available (needs HTTPS or localhost)', 'err') }
  }
</script>

<div class="logs">
  <div class="picker">
    <div class="fbox pickq"><Icon name="search" size={14} /><input placeholder="Find source…" bind:value={pickQ} /></div>
    <div class="plist">
      {#each pick as [appName, items]}
        <div class="papp">{appName}</div>
        {#each items as s (s.id)}
          <button class="pitem ghost" class:active={s.id === sourceId} onclick={() => select(s.id)} title={s.path}>
            <span class="kicon kind-{s.kind}"><Icon name={s.kind === 'docker' ? 'docker' : s.kind === 'journal' ? 'journal' : 'file'} size={13} /></span>
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
        <Select bind:value={unit} onchange={load} options={[{ value: '', label: 'All units' }, ...(source.units || []).map((u) => ({ value: u, label: u }))]} menuWidth={280} searchable label="Unit" />
      {/if}
      <Select bind:value={n} onchange={load} options={lineOpts} label="Lines" />
      <button class="follow" class:live={follow} onclick={toggleFollow} disabled={!sourceId}>
        {#if follow}<span class="pulse"></span> Live{:else}<Icon name="play" size={14} /> Follow{/if}
      </button>
      <button class="icon-btn" title="Reload" aria-label="Reload" onclick={load} disabled={!sourceId || loading}>{#if loading}<span class="spin"></span>{:else}<Icon name="refresh" size={16} />{/if}</button>
    </div>
    <div class="toolbar sub">
      <div class="fbox grow">
        <Icon name="filter" size={14} />
        <input placeholder="Filter shown lines…" bind:value={filter} />
      </div>
      <div class="lvls">
        {#each levelDefs as [lv, label]}
          <button class="lvchip {lv}" class:on={levels[lv]} aria-pressed={levels[lv]} onclick={() => (levels[lv] = !levels[lv])}><span class="ldot"></span>{label}</button>
        {/each}
      </div>
      <Switch bind:checked={wrap} label="Wrap" size="sm" />
      <span class="sep"></span>
      <button class="sm ghost icon-btn" onclick={copy} title="Copy shown lines" aria-label="Copy"><Icon name="copy" size={15} /></button>
      <button class="sm ghost icon-btn" onclick={dl} disabled={!sourceId} title="Download last 5000 lines" aria-label="Download"><Icon name="download" size={15} /></button>
      <button class="sm" onclick={() => go('/search', { source: sourceId })} disabled={!sourceId}><Icon name="search" size={14} /> Search all</button>
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
  .pickq { margin: 12px; }
  .fbox { position: relative; display: flex; align-items: center; min-width: 140px; }
  .fbox :global(.icon) { position: absolute; left: 10px; color: var(--dim); pointer-events: none; }
  .fbox input { width: 100%; padding-left: 31px; height: 30px; }
  .kicon { width: 22px; height: 22px; border-radius: 6px; display: grid; place-items: center; flex: none; }
  .kicon.kind-docker { color: #5aa8f0; background: color-mix(in srgb, #5aa8f0 14%, transparent); }
  .kicon.kind-file { color: #b78cf0; background: color-mix(in srgb, #b78cf0 14%, transparent); }
  .kicon.kind-journal { color: #4cc38a; background: color-mix(in srgb, #4cc38a 14%, transparent); }
  .lvls { display: inline-flex; gap: 4px; }
  .lvchip { height: 26px; padding: 0 9px; border-radius: 99px; font-size: 12px; gap: 6px; box-shadow: none; background: transparent; color: var(--dim); }
  .lvchip .ldot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; opacity: .5; }
  .lvchip.on { background: var(--field); color: var(--text); border-color: var(--border-strong); }
  .lvchip.on .ldot { opacity: 1; }
  .lvchip.error.on .ldot { background: var(--err); } .lvchip.warn.on .ldot { background: var(--warn); } .lvchip.info.on .ldot { background: var(--info); } .lvchip.debug.on .ldot { background: var(--debug); }
  .follow.live { background: color-mix(in srgb, var(--ok) 14%, var(--panel2)); border-color: color-mix(in srgb, var(--ok) 45%, transparent); color: var(--ok); font-weight: 600; }
  .pulse { width: 8px; height: 8px; border-radius: 50%; background: var(--ok); animation: pulse 1.4s infinite; }
  @keyframes pulse { 0% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--ok) 60%, transparent); } 70% { box-shadow: 0 0 0 7px transparent; } 100% { box-shadow: 0 0 0 0 transparent; } }
  .plist { overflow-y: auto; padding: 0 6px 12px; }
  .papp { font-size: 10.5px; font-weight: 650; text-transform: uppercase; letter-spacing: .07em; color: var(--dim); padding: 12px 8px 4px; }
  .pitem { width: 100%; height: 32px; padding: 0 8px; justify-content: flex-start; text-align: left; gap: 9px; font-size: 13px; font-weight: 450; color: var(--muted); }
  .pitem:hover:not(:disabled) { color: var(--text); }
  .pitem.active { background: var(--accent-soft); color: var(--text); font-weight: 560; }
  .sz { font-size: 11px; }
  .viewer { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
  .toolbar { display: flex; gap: 8px; align-items: center; padding: 10px 12px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
  .toolbar.sub { padding: 7px 12px; background: var(--panel); font-size: 12.5px; gap: 10px; }
  .title { flex: 1; display: flex; gap: 10px; align-items: baseline; min-width: 160px; padding-right: 40px; }
  .lv.error { color: var(--err); } .lv.warn { color: var(--warn); } .lv.info { color: var(--info); } .lv.debug { color: var(--debug); }
  .logbox { flex: 1; min-height: 0; padding: 6px 0; }
  .status { display: flex; gap: 12px; font-size: 11.5px; padding: 5px 12px; border-top: 1px solid var(--border); }
  .status .right { max-width: 50%; }
  @media (max-width: 860px) { .logs { grid-template-columns: 1fr; grid-template-rows: 180px 1fr; } .picker { border-right: 0; border-bottom: 1px solid var(--border); } }
</style>
