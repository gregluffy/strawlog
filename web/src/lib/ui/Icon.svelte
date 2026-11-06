<script>
  // Small stroke icon set (24px grid, currentColor).
  const P = {
    overview: 'M3 12h4l3-8 4 16 3-8h4',
    sources: 'M4 6h16M4 12h16M4 18h10',
    logs: 'M5 4h14v16H5zM8 8h8M8 12h8M8 16h5',
    search: 'M11 4a7 7 0 1 1 0 14 7 7 0 0 1 0-14zM16 16l4 4',
    health: 'M12 21s-7-4.5-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 11c0 5.5-7 10-7 10z',
    retention: 'M6 3h12M6 21h12M7 3v3a5 5 0 0 0 10 0V3M7 21v-3a5 5 0 0 1 10 0v3',
    nodes: 'M4 5h16v5H4zM4 14h16v5H4zM8 7.5h.01M8 16.5h.01',
    bell: 'M6 16V11a6 6 0 1 1 12 0v5l2 2H4zM10 20a2 2 0 0 0 4 0',
    users: 'M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM2 21v-1a6 6 0 0 1 12 0v1M17 11a3 3 0 1 0 0-6M22 21v-1a5 5 0 0 0-4-4.9',
    settings: 'M4 7h10M18 7h2M4 17h4M12 17h8M14 5v4M8 15v4',
    chevron: 'M6 9l6 6 6-6',
    chevronRight: 'M9 6l6 6-6 6',
    check: 'M5 12.5l4.5 4.5L19 7',
    x: 'M6 6l12 12M18 6L6 18',
    plus: 'M12 5v14M5 12h14',
    refresh: 'M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7',
    play: 'M8 5l11 7-11 7z',
    pause: 'M8 5v14M16 5v14',
    download: 'M12 4v11M7 10l5 5 5-5M5 20h14',
    copy: 'M9 9h10v11H9zM5 15V4h10',
    trash: 'M4 7h16M10 11v6M14 11v6M5 7l1 13h12l1-13M9 7V4h6v3',
    edit: 'M4 20h4L19 9l-4-4L4 16zM14 6l4 4',
    send: 'M4 12l16-8-6 16-3-7z',
    eye: 'M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12zM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z',
    filter: 'M4 5h16l-6 8v6l-4-2v-4z',
    logout: 'M15 4h4v16h-4M10 8l-4 4 4 4M6 12h11',
    menu: 'M4 7h16M4 12h16M4 17h16',
    alert: 'M12 4l9 16H3zM12 10v4M12 17h.01',
    info: 'M12 3a9 9 0 1 1 0 18 9 9 0 0 1 0-18zM12 11v6M12 7.5h.01',
    ok: 'M12 3a9 9 0 1 1 0 18 9 9 0 0 1 0-18zM8 12.5l3 3 5-6',
    server: 'M4 5h16v5H4zM4 14h16v5H4zM8 7.5h.01M8 16.5h.01',
    lock: 'M6 11h12v9H6zM9 11V8a3 3 0 0 1 6 0v3',
    key: 'M14 4a6 6 0 1 1-4.9 9.5L4 18.6V21h3v-2h2v-2h2l1.5-1.5A6 6 0 0 1 14 4zM16 8h.01',
    sun: 'M12 8a4 4 0 1 1 0 8 4 4 0 0 1 0-8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4',
    moon: 'M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z',
    monitor: 'M3 4h18v12H3zM8 20h8M12 16v4',
    docker: 'M3 13h18c0 4-3 7-9 7s-9-3-9-7zM5 9h3v3H5zM9 9h3v3H9zM13 9h3v3h-3zM9 5h3v3H9z',
    file: 'M6 3h8l4 4v14H6zM14 3v4h4',
    journal: 'M5 4h12a2 2 0 0 1 2 2v14H7a2 2 0 0 1-2-2zM5 18a2 2 0 0 1 2-2h12',
    clock: 'M12 3a9 9 0 1 1 0 18 9 9 0 0 1 0-18zM12 7v5l3 2',
    wrap: 'M4 6h16M4 12h13a3 3 0 0 1 0 6h-4M14 16l-2 2 2 2M4 18h5',
  }
  let { name, size = 16, stroke = 1.8, class: cls = '', style = '' } = $props()
</script>

<svg class="icon {cls}" {style} viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" stroke-width={stroke} stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={P[name] || ''} /></svg>

<style>
  .icon { flex: none; display: inline-block; vertical-align: middle; }
</style>
