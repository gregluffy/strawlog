<script>
  import { tick } from 'svelte'
  import Icon from './Icon.svelte'

  /**
   * options: [{ value, label, hint?, dot?: 'ok'|'err'|'warn'|'off', icon?, group?, disabled? }]
   * The menu is position:fixed so it is never clipped by scrolling tables or cards.
   */
  let {
    value = $bindable(),
    options = [],
    placeholder = 'Select…',
    onchange = undefined,
    disabled = false,
    size = 'md',
    icon = undefined,
    searchable = undefined,
    block = false,
    menuWidth = undefined,
    title = undefined,
    label = undefined,
    class: cls = '',
  } = $props()

  let open = $state(false)
  let query = $state('')
  let active = $state(0)
  let trigger = $state()
  let menu = $state()
  let searchEl = $state()
  let pos = $state({ top: 0, left: 0, width: 0, up: false, maxH: 300 })
  const uid = 'sel' + Math.random().toString(36).slice(2, 8)

  let isSearchable = $derived(searchable ?? options.length > 8)
  let selected = $derived(options.find((o) => o.value === value))
  let filtered = $derived.by(() => {
    const q = query.trim().toLowerCase()
    if (!q) return options
    return options.filter((o) => (o.label + ' ' + (o.hint || '') + ' ' + (o.group || '')).toLowerCase().includes(q))
  })

  function place() {
    const r = trigger.getBoundingClientRect()
    const below = window.innerHeight - r.bottom - 8
    const above = r.top - 8
    const up = below < 220 && above > below
    const w = Math.max(r.width, menuWidth || 0)
    let left = r.left
    if (left + w > window.innerWidth - 8) left = Math.max(8, window.innerWidth - 8 - w)
    pos = { top: up ? r.top - 4 : r.bottom + 4, left, width: w, up, maxH: Math.min(340, (up ? above : below) - 4) }
  }

  async function show() {
    if (disabled) return
    place()
    query = ''
    open = true
    active = Math.max(0, filtered.findIndex((o) => o.value === value))
    await tick()
    if (isSearchable) searchEl?.focus()
    else menu?.focus()
    scrollActive()
  }
  function hide(focus = true) {
    open = false
    if (focus) trigger?.focus()
  }
  function pick(o) {
    if (!o || o.disabled) return
    const changed = o.value !== value
    value = o.value
    hide()
    if (changed) onchange?.(o.value)
  }
  async function scrollActive() {
    await tick()
    menu?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }
  function move(d) {
    if (!filtered.length) return
    let i = active
    for (let n = 0; n < filtered.length; n++) {
      i = (i + d + filtered.length) % filtered.length
      if (!filtered[i].disabled) break
    }
    active = i
    scrollActive()
  }
  let typed = ''
  let typedAt = 0
  function onMenuKey(e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); move(1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1) }
    else if (e.key === 'Home') { e.preventDefault(); active = 0; scrollActive() }
    else if (e.key === 'End') { e.preventDefault(); active = filtered.length - 1; scrollActive() }
    else if (e.key === 'Enter') { e.preventDefault(); pick(filtered[active]) }
    else if (e.key === 'Escape') { e.preventDefault(); hide() }
    else if (e.key === 'Tab') hide(false)
    else if (!isSearchable && e.key.length === 1) {
      const now = Date.now()
      typed = now - typedAt > 700 ? e.key.toLowerCase() : typed + e.key.toLowerCase()
      typedAt = now
      const i = filtered.findIndex((o) => o.label.toLowerCase().startsWith(typed))
      if (i >= 0) { active = i; scrollActive() }
    }
  }
  function onTriggerKey(e) {
    if (['ArrowDown', 'ArrowUp', 'Enter', ' '].includes(e.key)) { e.preventDefault(); show() }
  }
  function onWindow(e) {
    if (!open) return
    if (e.type === 'resize') return hide(false)
    if (e.type === 'scroll' && menu && menu.contains(e.target)) return
    if (e.type === 'scroll') return place()
    if (!trigger.contains(e.target) && !menu?.contains(e.target)) hide(false)
  }
</script>

<svelte:window onmousedown={onWindow} onresize={onWindow} onscrollcapture={onWindow} />

<button
  type="button"
  bind:this={trigger}
  class="sel {size} {cls}"
  class:block
  class:open
  class:placeholder={!selected}
  {disabled}
  {title}
  aria-haspopup="listbox"
  aria-expanded={open}
  aria-controls={uid}
  aria-label={label}
  onclick={() => (open ? hide() : show())}
  onkeydown={onTriggerKey}
>
  {#if icon}<Icon name={icon} size={15} class="lead" />{/if}
  {#if selected?.dot}<span class="sdot {selected.dot}"></span>{/if}
  <span class="val">{selected ? selected.label : placeholder}</span>
  <Icon name="chevron" size={14} class="chev" />
</button>

{#if open}
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div
    bind:this={menu}
    id={uid}
    class="menu"
    class:up={pos.up}
    role="listbox"
    tabindex="0"
    style="left:{pos.left}px; {pos.up ? `bottom:${window.innerHeight - pos.top}px` : `top:${pos.top}px`}; min-width:{pos.width}px; max-width:{Math.max(pos.width, window.innerWidth - pos.left - 8)}px; max-height:{pos.maxH}px"
    onkeydown={onMenuKey}
    aria-activedescendant={filtered[active] ? uid + '-' + active : undefined}
  >
    {#if isSearchable}
      <div class="search">
        <Icon name="search" size={14} />
        <input bind:this={searchEl} bind:value={query} placeholder="Type to filter…" onkeydown={onMenuKey} oninput={() => (active = 0)} />
      </div>
    {/if}
    <div class="opts">
      {#each filtered as o, i (o.value)}
        {#if o.group && (i === 0 || filtered[i - 1].group !== o.group)}
          <div class="grp">{o.group}</div>
        {/if}
        <div
          id="{uid}-{i}"
          class="opt"
          class:sel-opt={o.value === value}
          class:disabled={o.disabled}
          data-active={i === active}
          role="option"
          aria-selected={o.value === value}
          aria-disabled={o.disabled}
          tabindex="-1"
          onmousemove={() => (active = i)}
          onclick={() => pick(o)}
          onkeydown={() => {}}
        >
          {#if o.dot}<span class="sdot {o.dot}"></span>{:else if o.icon}<Icon name={o.icon} size={15} />{/if}
          <span class="lbl">
            <span class="t">{o.label}</span>
            {#if o.hint}<span class="h">{o.hint}</span>{/if}
          </span>
          {#if o.value === value}<Icon name="check" size={15} class="tick" />{/if}
        </div>
      {:else}
        <div class="none">No matches</div>
      {/each}
    </div>
  </div>
{/if}

<style>
  .sel {
    display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%;
    background: var(--field); border: 1px solid var(--border); border-radius: var(--r-sm);
    color: var(--text); padding: 0 10px; height: 34px; font: inherit; cursor: pointer; text-align: left;
    transition: border-color .12s, background .12s, box-shadow .12s;
  }
  .sel.sm { height: 28px; padding: 0 8px; font-size: 12.5px; gap: 6px; }
  .sel.block { display: flex; width: 100%; }
  .sel:hover:not(:disabled) { border-color: var(--border-strong); background: var(--field-hover); }
  .sel.open { border-color: var(--accent); box-shadow: 0 0 0 3px var(--ring); }
  .sel:disabled { opacity: .55; cursor: default; }
  .val { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .placeholder .val { color: var(--dim); }
  .sel :global(.chev) { color: var(--dim); transition: transform .15s; }
  .sel.open :global(.chev) { transform: rotate(180deg); color: var(--accent); }
  .sel :global(.lead) { color: var(--muted); }

  .menu {
    position: fixed; z-index: 1000; display: flex; flex-direction: column;
    background: var(--popover); border: 1px solid var(--border-strong); border-radius: var(--r-md);
    box-shadow: var(--shadow-lg); padding: 4px; outline: none; overflow: hidden;
    animation: pop .12s ease-out; transform-origin: top;
  }
  .menu.up { transform-origin: bottom; }
  @keyframes pop { from { opacity: 0; transform: scale(.97) translateY(-2px); } }
  .search { display: flex; align-items: center; gap: 6px; padding: 4px 8px 6px; margin-bottom: 4px; border-bottom: 1px solid var(--border); color: var(--dim); }
  .search input { border: 0; background: transparent; padding: 4px 0; flex: 1; box-shadow: none; outline: none; height: auto; }
  .opts { overflow-y: auto; min-height: 0; }
  .grp { font-size: 10.5px; text-transform: uppercase; letter-spacing: .06em; color: var(--dim); padding: 8px 10px 3px; font-weight: 600; }
  .opt { display: flex; align-items: center; gap: 9px; padding: 7px 10px; border-radius: var(--r-xs); cursor: pointer; font-size: 13px; color: var(--text); }
  .opt[data-active='true'] { background: var(--hover); }
  .opt.sel-opt .t { font-weight: 600; }
  .opt.disabled { opacity: .45; cursor: default; }
  .opt :global(.tick) { color: var(--accent); margin-left: auto; }
  .lbl { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .lbl .t { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .lbl .h { font-size: 11.5px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .none { padding: 12px; color: var(--dim); text-align: center; font-size: 12.5px; }
  .sdot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--dim); }
  .sdot.ok { background: var(--ok); box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 22%, transparent); }
  .sdot.err { background: var(--err); }
  .sdot.warn { background: var(--warn); }
</style>
