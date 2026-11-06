<script>
  import { app } from '../lib/state.svelte.js'
  import { api } from '../lib/api.js'

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
      <svg viewBox="0 0 32 32" width="34" height="34"><rect width="32" height="32" rx="7" fill="var(--accent)" /><path d="M8 10h16M8 16h11M8 22h14" stroke="var(--accent-ink)" stroke-width="3" stroke-linecap="round" /></svg>
      <div><h1>Straw Log</h1><p class="sub">{app.setupRequired ? 'First-run setup' : 'Sign in'}</p></div>
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
    {#if error}<div class="alert err">{error}</div>{/if}
    <button class="primary" disabled={busy}>{#if busy}<span class="spin"></span>{/if}{app.setupRequired ? 'Create password' : 'Sign in'}</button>
  </form>
</div>

<style>
  .wrap { min-height: 100%; display: grid; place-items: center; padding: 16px; }
  .box { width: 100%; max-width: 380px; display: flex; flex-direction: column; gap: 14px; padding: 26px; }
  .brand { display: flex; gap: 12px; align-items: center; margin-bottom: 4px; }
  .small { font-size: 12.5px; margin: 0; }
  button { justify-content: center; padding: 8px; }
</style>
