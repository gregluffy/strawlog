<script>
  import { api } from '../lib/api.js'
  import { app, save } from '../lib/state.svelte.js'
  import Segmented from '../lib/ui/Segmented.svelte'
  import { toast } from '../lib/ui/overlay.svelte.js'

  let current = $state('')
  let next = $state('')
  let next2 = $state('')
  let msg = $state('')
  let error = $state('')
  let theme = $state((() => { try { return localStorage.getItem('strawlog.theme') || 'auto' } catch { return 'auto' } })())

  $effect(() => {
    if (theme === 'auto') document.documentElement.removeAttribute('data-theme')
    else document.documentElement.setAttribute('data-theme', theme)
    save('strawlog.theme', theme)
  })

  async function change(e) {
    e.preventDefault()
    msg = error = ''
    if (next !== next2) { error = 'New passwords do not match'; return }
    try {
      await api.post('/api/password', { current, new: next })
      toast('Password changed. Your other sessions were signed out.')
      current = next = next2 = ''
    } catch (err) { error = err.message }
  }
</script>

<div class="page">
  <div class="page-head"><div><h1>Settings</h1><p class="sub">Straw Log v{app.version}</p></div></div>
  <div class="grid cols-2">
    <form class="card form" onsubmit={change}>
      <h2>Your password</h2>
      <p class="muted small" style="margin:0">Signed in as <b>{app.user?.username}</b> ({app.user?.role}). Changing it signs out your other sessions.</p>
      <label class="field">Current password <input type="password" bind:value={current} autocomplete="current-password" required /></label>
      <label class="field">New password (min. 10) <input type="password" bind:value={next} autocomplete="new-password" minlength="10" required /></label>
      <label class="field">Repeat new password <input type="password" bind:value={next2} autocomplete="new-password" required /></label>
      {#if error}<div class="alert err">{error}</div>{/if}
      {#if msg}<div class="alert ok">{msg}</div>{/if}
      <div><button class="primary">Change password</button></div>
      <p class="dim small" style="margin:0">Locked out? <code>docker exec -it strawlog strawlog hub set-password &lt;user&gt;</code></p>
    </form>
    <div class="card form">
      <h2>Appearance</h2>
      <div class="field">Theme
        <Segmented bind:value={theme} label="Theme" options={[{ value: 'auto', label: 'System', icon: 'monitor' }, { value: 'dark', label: 'Dark', icon: 'moon' }, { value: 'light', label: 'Light', icon: 'sun' }]} />
      </div>
      <h2 style="margin-top:12px">Security model</h2>
      <ul class="muted small list">
        <li>Hub ↔ agent: TLS 1.3 with mutual certificates, both sides pinned by SHA-256 fingerprint.</li>
        <li>Agents answer only paired hubs; unknown IPs can be dropped before the handshake (<code>STRAWLOG_ALLOW_FROM</code>).</li>
        <li>Agents are read-only unless <code>STRAWLOG_ALLOW_WRITE=true</code>, and even then only inside <code>STRAWLOG_WRITE_PATHS</code>.</li>
        <li>Docker access goes through a socket proxy that only allows reading containers and logs.</li>
        <li>Dashboard: users with roles (viewer / operator / admin) enforced server-side, PBKDF2-hashed passwords, HttpOnly SameSite=Strict session cookie, CSRF header check, login rate limiting per IP and per user, strict CSP.</li>
      </ul>
    </div>
  </div>
</div>

<style>
  div.field { align-items: flex-start; }
  .form { display: flex; flex-direction: column; gap: 12px; }
  .small { font-size: 12.5px; }
  .list { margin: 0; padding-left: 18px; display: flex; flex-direction: column; gap: 6px; }
</style>
