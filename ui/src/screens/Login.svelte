<script>
  import { api } from '../lib/api.js'
  import { notify } from '../lib/stores.js'
  import StateBlock from '../lib/components/StateBlock.svelte'

  let { setup = false, onSuccess = null } = $props()

  let email = $state('')
  let password = $state('')
  let error = $state('')
  let busy = $state(false)
  let isSetup = $state(false)

  $effect(() => {
    isSetup = setup
  })

  async function submit(event) {
    event.preventDefault()
    error = ''
    if (!email.trim() || !password) {
      error = 'Introduce el correo electrónico y la contraseña.'
      return
    }
    if (isSetup && password.length < 8) {
      error = 'La contraseña debe tener al menos 8 caracteres.'
      return
    }
    busy = true
    try {
      if (isSetup) {
        await api('/api/admin/setup', {
          method: 'POST',
          auth: false,
          body: { email: email.trim(), password },
        })
      }
      const session = await api('/api/admin/auth-with-password', {
        method: 'POST',
        auth: false,
        body: { email: email.trim(), password },
      })
      password = ''
      await onSuccess?.(session)
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
      if (err?.code === 'setup_complete') {
        isSetup = false
        notify('El superusuario ya existe. Inicia sesión.', 'info')
      } else {
        notify(error, 'error')
      }
    } finally {
      busy = false
    }
  }
</script>

<main class="auth-layout">
  <div class="auth-card">
    <div class="auth-head">
      <div class="auth-logo" aria-hidden="true">DK</div>
      <h1>{isSetup ? 'Configuración inicial' : 'Bienvenido de nuevo'}</h1>
      <p class="auth-subtitle">
        {isSetup
          ? 'Crea el primer superusuario de esta instancia.'
          : 'Accede al panel de administración de Doketfire.'}
      </p>
    </div>

    {#if isSetup}
      <div class="callout callout-info mb-2">
        <span aria-hidden="true">ℹ</span>
        <span>Esta pantalla se desactiva permanentemente tras crear el superusuario.</span>
      </div>
    {/if}

    <form class="form-stack" onsubmit={submit}>
      <div class="field">
        <label class="field-label" for="auth-email">Correo electrónico</label>
        <input
          id="auth-email"
          class="input"
          type="email"
          autocomplete="username"
          placeholder="admin@ejemplo.com"
          bind:value={email}
          required
        />
      </div>
      <div class="field">
        <label class="field-label" for="auth-password">
          Contraseña{isSetup ? ' (mínimo 8 caracteres)' : ''}
        </label>
        <input
          id="auth-password"
          class="input"
          type="password"
          autocomplete={isSetup ? 'new-password' : 'current-password'}
          placeholder="••••••••"
          minlength={isSetup ? 8 : undefined}
          bind:value={password}
          required
        />
      </div>

      {#if error}
        <StateBlock kind="inline" title={error} />
      {/if}

      <button class="btn btn-primary btn-block" type="submit" disabled={busy}>
        {#if busy}<span class="spinner" aria-hidden="true"></span>{/if}
        {isSetup ? 'Crear superusuario' : 'Iniciar sesión'}
      </button>
    </form>
  </div>
</main>
