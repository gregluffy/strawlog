<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { bytes, ago, pct } from '../lib/fmt.js'
  import { app, go, nodeName } from '../lib/state.svelte.js'

  let sources = $state([])
  let loading = $state(false)
  let error = $state('')
  let q = $state(app.route.params.app || '')
  let kind = $state('')
  let sort = $state('size')
  let collapsed = $state({})
  let expanded = $state({})

  async function load(refresh = false) {
    loading = true
    try {
      sources = await api.get(api.n(app.node, '/sources' + (refresh ? '?refresh=1' : '')))
      error = ''
    } catch (e) { error = e.message; sources = [] }
    loading = false
  }
  onMount(() => load())
  let lastNode = app.node
  $effect(() => { if (app.node !== lastNode) { lastNode = app.node; load() } })

  let groups = $derived.by(() => {
    const ql = q.toLowerCase()
    const m = new Map()
    for (const s of sources) {
      if (kind && s.kind !== kind) continue
      if (ql && !(s.app.toLowerCase().includes(ql) || s.name.toLowerCase().includes(ql) || (s.path || '').toLowerCase().includes(ql))) continue
      if (!m.has(s.app)) m.set(s.app, { app: s.app, items: [], total: 0, growth: 0 })
      const g = m.get(s.app)
      g.items.push(s)
      g.total += s.totalSize
      g.growth += s.growth24h || 0
    }
    const arr = [...m.values()]
    const key = sort === 'size' ? (x) => -x.total : sort === 'growth' ? (x) => -x.growth : (x) => x.app
    arr.sort((a, b) => (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0))
    for (const g of arr) g.items.sort((a, b) => (sort === 'name' ? a.name.localeCompare(b.name) : sort === 'growth' ? (b.growth24h || 0) - (a.growth24h || 0) : b.totalSize - a.totalSize))
    return arr
  })
  let grand = $derived(groups.reduce((a, g) => a + g.total, 0))
  let maxApp = $derived(Math.max(1, ...groups.map((g) => g.total)))

  function unbounded(s) { return s.kind === 'docker' && s.logDriver === 'json-file' && !(s.logOpts && s.logOpts['max-size']) }
</script>

<div class="page">
  <div class="page-head">
    <div><h1>Sources</h1><p class="sub">Every log on <b>{nodeName(app.node)}</b>, grouped by application. {groups.length} apps · {bytes(grand)}</p></div>
    <div class="row">
      <input placeholder="Filter app, name or path…" bind:value={q} style="width:240px" />
      <select bind:value={kind}>
        <option value="">All kinds</option>
        <option value="docker">Docker</option>
        <option value="file">Files</option>
        <option value="journal">Journald</option>
      </select>
      <select bind:value={sort}>
        <option value="size">Sort: size</option>
        <option value="growth">Sort: growth</option>
        <option value="name">Sort: name</option>
      </select>
      <button onclick={() => load(true)} disabled={loading}>{#if loading}<span class="spin"></span>{/if} Rescan</button>
    </div>
  </div>

  {#if error}<div class="alert err" style="margin-bottom:12px">{error}</div>{/if}

  {#if !loading && groups.length === 0 && !error}
    <div class="card empty">No sources match.</div>
  {/if}

  <div class="groups">
    {#each groups as g (g.app)}
      <div class="card tight">
        <button class="ghead ghost" onclick={() => (collapsed[g.app] = !collapsed[g.app])}>
          <span class="caret">{collapsed[g.app] ? '▸' : '▾'}</span>
          <b class="trunc">{g.app}</b>
          <span class="dim">{g.items.length} source{g.items.length > 1 ? 's' : ''}</span>
          <span class="bar gbar"><i style="width:{pct(g.total, maxApp)}%"></i></span>
          <span class="num">{bytes(g.total)}</span>
          <span class="num dim gw">{g.growth > 0 ? '+' + bytes(g.growth) + '/d' : ''}</span>
        </button>
        {#if !collapsed[g.app]}
          <div style="overflow-x:auto">
            <table>
              <thead><tr><th>Source</th><th>Kind</th><th class="num">Active</th><th class="num">Rotated</th><th class="num">Total</th><th class="num">24h</th><th>Updated</th><th></th></tr></thead>
              <tbody>
                {#each g.items as s (s.id)}
                  <tr class="click" onclick={() => go('/logs', { source: s.id })}>
                    <td style="max-width:420px">
                      <div class="trunc"><b>{s.name}</b>
                        {#if unbounded(s)}<span class="badge warn" title="json-file without max-size">no limit</span>{/if}
                        {#if s.state && s.state !== 'running'}<span class="badge">{s.state}</span>{/if}
                        {#if !s.readable}<span class="badge err">unreadable</span>{/if}
                      </div>
                      <div class="dim mono trunc small" title={s.path}>{s.path || ''}</div>
                    </td>
                    <td><span class="badge kind-{s.kind}">{s.kind}</span> <span class="dim small">{s.origin === 'docker' ? s.logDriver : s.origin}</span></td>
                    <td class="num">{bytes(s.size)}</td>
                    <td class="num">
                      {#if s.rotated?.length}
                        <button class="ghost sm" onclick={(e) => { e.stopPropagation(); expanded[s.id] = !expanded[s.id] }}>{s.rotated.length} · {bytes(s.totalSize - s.size)}</button>
                      {:else}<span class="dim">–</span>{/if}
                    </td>
                    <td class="num"><b>{bytes(s.totalSize)}</b></td>
                    <td class="num">{s.growth24h > 0 ? '+' + bytes(s.growth24h) : '–'}</td>
                    <td class="muted small">{ago(s.mtime)}</td>
                    <td class="num">
                      <button class="sm" onclick={(e) => { e.stopPropagation(); go('/search', { source: s.id }) }}>Search</button>
                      <button class="sm" onclick={(e) => { e.stopPropagation(); go('/policies', { source: s.id }) }} title="Create a retention policy for this source">Policy</button>
                    </td>
                  </tr>
                  {#if expanded[s.id]}
                    <tr><td colspan="8" class="rot">
                      {#each s.rotated as r}
                        <div class="row small mono"><span class="trunc grow">{r.path}</span><span class="muted">{r.compressed ? 'gz' : ''}</span><span class="num" style="width:80px">{bytes(r.size)}</span><span class="muted" style="width:80px">{ago(r.mtime)}</span></div>
                      {/each}
                    </td></tr>
                  {/if}
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .groups { display: flex; flex-direction: column; gap: 10px; }
  .ghead { width: 100%; display: grid; grid-template-columns: 14px minmax(80px, 260px) 90px 1fr 80px 90px; gap: 12px; align-items: center; padding: 11px 14px; border-radius: 0; text-align: left; }
  .gbar { min-width: 60px; }
  .caret { color: var(--dim); }
  .num { text-align: right; font-variant-numeric: tabular-nums; }
  .gw { font-size: 12px; }
  .small { font-size: 12px; }
  .rot { background: var(--bg2); padding: 8px 18px; }
  td .badge { margin-left: 6px; }
  @media (max-width: 760px) { .ghead { grid-template-columns: 14px 1fr 80px; } .ghead .dim, .gbar, .gw { display: none; } }
</style>
