<script>
  import { onMount } from 'svelte';
  import { api, errorMessage } from '../lib/api.js';
  import { formatDate } from '../lib/format.js';
  import { notify } from '../lib/stores.js';
  import StateBlock from '../lib/components/StateBlock.svelte';
  import Icon from '../lib/components/Icon.svelte';

  let loaded = $state(false);
  let loading = $state(true);
  let error = $state('');
  let saving = $state(false);
  let original = $state(null);
  let form = $state({ title: '', corsOrigins: '', authRateLimit: 10, logRetention: 500 });
  let validation = $state('');

  function fromSettings(data) {
    original = data;
    form = {
      title: data?.title || '',
      corsOrigins: Array.isArray(data?.corsOrigins) ? data.corsOrigins.join('\n') : '',
      authRateLimit: Number(data?.authRateLimit) || 10,
      logRetention: Number(data?.logRetention) || 500,
    };
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const data = await api('/api/admin/settings', { key: 'admin-settings' });
      fromSettings(data);
      loaded = true;
    } catch (err) {
      error = errorMessage(err);
      loaded = true;
    } finally {
      loading = false;
    }
  }

  function validate() {
    const title = form.title.trim();
    if (!title || Array.from(title).length > 80) return 'El título debe tener entre 1 y 80 caracteres.';
    const rate = Number(form.authRateLimit);
    if (!Number.isInteger(rate) || rate < 1 || rate > 1000) return 'El límite de autenticación debe estar entre 1 y 1000.';
    const retention = Number(form.logRetention);
    if (!Number.isInteger(retention) || retention < 50 || retention > 5000) return 'La retención de registros debe estar entre 50 y 5000.';
    const origins = form.corsOrigins.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
    for (const origin of origins) {
      try {
        const url = new URL(origin);
        if (!['http:', 'https:'].includes(url.protocol) || !url.host) throw new Error('invalid');
      } catch {
        return `Origen CORS no válido: «${origin}». Usa http:// o https:// con dominio.`;
      }
    }
    return '';
  }

  async function save(event) {
    event?.preventDefault?.();
    validation = validate();
    if (validation) return;
    saving = true;
    try {
      const data = await api('/api/admin/settings', {
        method: 'PATCH',
        body: {
          title: form.title.trim(),
          corsOrigins: form.corsOrigins.split(/\r?\n/).map((s) => s.trim()).filter(Boolean),
          authRateLimit: Number(form.authRateLimit),
          logRetention: Number(form.logRetention),
        },
      });
      fromSettings(data);
      document.title = `${data.title || 'Doketfire'} — Panel de administración`;
      notify('Ajustes guardados.', 'success');
    } catch (err) {
      notify(errorMessage(err), 'error');
    } finally {
      saving = false;
    }
  }

  function reset() {
    if (original) fromSettings(original);
    validation = '';
  }

  onMount(load);
</script>

<section class="page">
  <header class="page-head">
    <div>
      <h1>Ajustes</h1>
      <p class="subtitle">Configuración general del panel de administración.</p>
    </div>
  </header>

  {#if error}
    <StateBlock kind="error" title="No se pudieron cargar los ajustes" detail={error} onRetry={load} />
  {:else if loading && !loaded}
    <StateBlock kind="loading" title="Cargando ajustes…" />
  {:else}
    <form class="panel settings-form" onsubmit={save}>
      <label class="field">
        <span>Título del panel</span>
        <input type="text" maxlength="80" bind:value={form.title} required />
        <small>Máximo 80 caracteres.</small>
      </label>

      <label class="field">
        <span>Orígenes CORS permitidos</span>
        <textarea
          rows="4"
          placeholder={'https://app.ejemplo.com\nhttps://admin.ejemplo.com'}
          bind:value={form.corsOrigins}
        ></textarea>
        <small>Un origen por línea. Deben incluir esquema http:// o https://.</small>
      </label>

      <div class="field-row">
        <label class="field">
          <span>Límite de autenticación (intentos/minuto)</span>
          <input type="number" min="1" max="1000" step="1" bind:value={form.authRateLimit} required />
        </label>
        <label class="field">
          <span>Retención de registro (entradas)</span>
          <input type="number" min="50" max="5000" step="10" bind:value={form.logRetention} required />
        </label>
      </div>

      {#if validation}
        <p class="validation" role="alert">{validation}</p>
      {/if}

      <div class="form-actions">
        <button class="btn primary" type="submit" disabled={saving}>
          <Icon name="check" size={15} /> {saving ? 'Guardando…' : 'Guardar ajustes'}
        </button>
        <button class="btn ghost" type="button" onclick={reset} disabled={saving}>Descartar cambios</button>
        {#if original?.updatedAt}
          <span class="muted updated">Actualizado: {formatDate(original.updatedAt)}</span>
        {/if}
      </div>
    </form>

    <div class="panel info">
      <h2>Notas</h2>
      <ul>
        <li>Los cambios de CORS se aplican a las siguientes solicitudes.</li>
        <li>El límite de autenticación protege los endpoints de inicio de sesión y registro.</li>
        <li>La retención controla cuántas entradas del registro se conservan en memoria.</li>
      </ul>
    </div>
  {/if}
</section>

<style>
  .settings-form { display: grid; gap: var(--space-md); max-width: 720px; }
  .field-row { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-md); }
  .validation { color: var(--danger); font-size: 0.85rem; margin: 0; }
  .form-actions { display: flex; align-items: center; gap: var(--space-sm); flex-wrap: wrap; }
  .updated { font-size: 0.8rem; margin-left: auto; }
  .info { max-width: 720px; margin-top: var(--space-lg); }
  .info h2 { margin-top: 0; font-size: 1rem; }
  .info ul { margin: 0; padding-left: 1.2rem; color: var(--text-muted); font-size: 0.85rem; }
  @media (max-width: 600px) {
    .field-row { grid-template-columns: 1fr; }
  }
</style>
