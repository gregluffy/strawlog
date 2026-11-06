<script>
  import { app } from '../lib/state.svelte.js'
  import { api } from '../lib/api.js'
  import Icon from '../lib/ui/Icon.svelte'

  let username = $state(app.setupRequired ? 'admin' : '')
  let password = $state('')
  let password2 = $state('')
  let token = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(e) {
    e.preventDefault()
    error = ''
    if (app.setupRequired && password !== password2) { error = 'Passwords do not match'; return }
    busy = true
    try {
      if (app.setupRequired) {
        const r = await api.post('/api/setup', { token, username, password })
        app.setupRequired = false
        app.user = r.user
      } else {
        const r = await api.post('/api/login', { username, password })
        app.user = r.user
      }
      app.authenticated = true
    } catch (err) {
      error = err.message
    } finally {
      busy = false
    }
  }
</script>

<div class="wrap">
  <form class="card box" onsubmit={submit}>
    <div class="brand">
      <svg viewBox="0 0 32 32" width="40" height="40"><rect width="32" height="32" rx="7" fill="var(--accent)" /><path d="M8 10h16M8 16h11M8 22h14" stroke="var(--accent-ink)" stroke-width="3" stroke-linecap="round" /></svg>
      <div><h1>Straw Log</h1><p class="sub">{app.setupRequired ? 'Create the first admin account' : 'Sign in to your log dashboard'}</p></div>
    </div>
    {#if app.setupRequired}
      <p class="muted small">Find the setup token in the hub logs: <code>docker logs strawlog</code></p>
      <label class="field">Setup token <input bind:value={token} autocomplete="off" required /></label>
      <label class="field">Admin username <input bind:value={username} autocomplete="username" required /></label>
      <label class="field">Admin password (min. 10 characters) <input type="password" bind:value={password} autocomplete="new-password" minlength="10" required /></label>
      <label class="field">Repeat password <input type="password" bind:value={password2} autocomplete="new-password" required /></label>
    {:else}
      <!-- svelte-ignore a11y_autofocus -->
      <label class="field">Username <input bind:value={username} autocomplete="username" autofocus required /></label>
      <label class="field">Password <input type="password" bind:value={password} autocomplete="current-password" required /></label>
    {/if}
    {#if error}<div class="alert err"><Icon name="alert" size={16} /><span>{error}</span></div>{/if}
    <button class="primary" disabled={busy}>{#if busy}<span class="spin"></span>{/if}{app.setupRequired ? 'Create password' : 'Sign in'}</button>
    {#if !app.setupRequired}
      <p class="hint dim">Upgraded from v0.1 (password only)? Your username is <code>admin</code>.</p>
    {/if}
  </form>
  <p class="foot dim">Self-hosted · your logs never leave your servers</p>
</div>

<style>
  .wrap { min-height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 18px; padding: 16px;
    background: radial-gradient(600px 400px at 50% 30%, color-mix(in srgb, var(--accent) 9%, transparent), transparent 70%); }
  .box { width: 100%; max-width: 390px; display: flex; flex-direction: column; gap: 16px; padding: 30px; box-shadow: var(--shadow-lg); }
  .brand { display: flex; gap: 14px; align-items: center; margin-bottom: 6px; }
  .brand h1 { margin: 0; }
  .foot { font-size: 12px; margin: 0; }
  .hint { font-size: 12px; margin: -4px 0 0; text-align: center; }
  .small { font-size: 12.5px; margin: 0; }
  button { height: 40px; margin-top: 4px; }
</style>
