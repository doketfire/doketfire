<script>
  import { api, downloadBlob, errorMessage } from '../lib/api.js';
  import { formatBytes, formatNumber } from '../lib/format.js';
  import { notify } from '../lib/stores.js';
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte';
  import StateBlock from '../lib/components/StateBlock.svelte';
  import Icon from '../lib/components/Icon.svelte';

  let check = $state(null);
  let loadingCheck = $state(true);
  let checkError = $state('');
  let busy = $state('');
  let confirmRestore = $state(false);
  let restoreFile = $state(null);
  let compactInfo = $state(null);

  async function loadCheck() {
    loadingCheck = true;
    checkError = '';
    try {
      check = await api('/api/admin/check', { method: 'POST' });
    } catch (err) {
      checkError = errorMessage(err);
    } finally {
      loadingCheck = false;
    }
  }

  async function runBackup() {
    busy = 'backup';
    try {
      await downloadBlob('/api/admin/backup', 'doketfire-backup.db');
      notify('Copia de seguridad descargada.', 'success');
    } catch (err) {
      notify(errorMessage(err), 'error');
    } finally {
      busy = '';
    }
  }

  async function runCompact() {
    busy = 'compact';
    try {
      compactInfo = await api('/api/admin/compact', { method: 'POST', body: {} });
      notify('Compactación completada.', 'success');
      await loadCheck();
    } catch (err) {
      notify(errorMessage(err), 'error');
    } finally {
      busy = '';
    }
  }

  function pickRestoreFile(event) {
    restoreFile = event.currentTarget?.files?.[0] || null;
  }

  async function runRestore() {
    if (!restoreFile) {
      notify('Selecciona un archivo de copia de seguridad.', 'error');
      return;
    }
    busy = 'restore';
    confirmRestore = false;
    try {
      const form = new FormData();
      form.append('file', restoreFile);
      const report = await api('/api/admin/restore', { method: 'POST', body: form });
      compactInfo = report;
      notify('Restauración completada.', 'success');
      await loadCheck();
    } catch (err) {
      notify(errorMessage(err), 'error');
    } finally {
      busy = '';
      restoreFile = null;
      const input = document.getElementById('restore-file-input');
      if (input) input.value = '';
    }
  }

  function issueText(issue) {
    if (typeof issue === 'string') return issue;
    if (!issue) return 'Incidencia desconocida';
    const parts = [issue.kind, issue.message, issue.collection, issue.index, issue.docId].filter(Boolean);
    return parts.join(' · ');
  }

  $effect(() => { loadCheck(); });
</script>

<section class="page">
  <header class="page-head">
    <div>
      <h1>Mantenimiento</h1>
      <p class="subtitle">Comprobación de integridad, copias de seguridad y compactación.</p>
    </div>
    <button class="btn ghost" onclick={loadCheck} disabled={loadingCheck} title="Volver a comprobar">
      <Icon name="refresh" size={15} /> Comprobar
    </button>
  </header>

  <div class="grid-2">
    <div class="panel section">
      <h2>Estado de la base de datos</h2>
      {#if checkError}
        <StateBlock kind="error" title="No se pudo comprobar la base de datos" detail={checkError} onRetry={loadCheck} />
      {:else if loadingCheck}
        <StateBlock kind="loading" title="Comprobando integridad…" />
      {:else if check}
        <dl class="details">
          <div><dt>Estado</dt><dd class:ok={check.issues?.length === 0} class:bad={check.issues?.length > 0}>{check.issues?.length === 0 ? 'Correcto' : 'Incidencias'}</dd></div>
          <div><dt>Colecciones</dt><dd>{formatNumber(check.collections ?? 0)}</dd></div>
          <div><dt>Documentos</dt><dd>{formatNumber(check.documents ?? 0)}</dd></div>
          <div><dt>Entradas de índice</dt><dd>{formatNumber(check.indexEntries ?? 0)}</dd></div>
          {#if check.truncated}<div><dt>Resultado</dt><dd>Truncado</dd></div>{/if}
        </dl>
        {#if check.issues?.length}
          <div class="issues">
            <strong>Incidencias</strong>
            <ul>
              {#each check.issues as issue}
                <li>{issueText(issue)}</li>
              {/each}
            </ul>
          </div>
        {/if}
      {/if}
    </div>

    <div class="panel section">
      <h2>Copias de seguridad</h2>
      <p class="muted">Descarga una copia completa de la base de datos antes de realizar cambios importantes.</p>
      <div class="actions">
        <button class="btn primary" onclick={runBackup} disabled={busy !== ''}>
          <Icon name="download" size={15} /> {busy === 'backup' ? 'Preparando…' : 'Descargar copia'}
        </button>
      </div>
      <div class="divider"></div>
      <h3>Restaurar</h3>
      <p class="muted">Esta acción reemplaza la base de datos actual. No se puede deshacer.</p>
      <label class="field">
        <span>Archivo de copia (.db)</span>
        <input id="restore-file-input" type="file" accept=".db,application/octet-stream" onchange={pickRestoreFile} />
      </label>
      <button class="btn danger" onclick={() => (confirmRestore = true)} disabled={!restoreFile || busy !== ''}>
        <Icon name="trash" size={15} /> Restaurar copia
      </button>
      {#if compactInfo}
        <div class="result ok">
          Última operación: {formatBytes(compactInfo.before)} → {formatBytes(compactInfo.after)}
          {#if compactInfo.backup}· copia: {compactInfo.backup}{/if}
        </div>
      {/if}
    </div>

    <div class="panel section">
      <h2>Compactación</h2>
      <p class="muted">Reduce el tamaño del archivo de base de datos sin perder datos.</p>
      <button class="btn" onclick={runCompact} disabled={busy !== ''}>
        <Icon name="refresh" size={15} /> {busy === 'compact' ? 'Compactando…' : 'Compactar base de datos'}
      </button>
    </div>
  </div>
</section>

<ConfirmDialog
  open={confirmRestore}
  title="Restaurar copia de seguridad"
  message={`Se reemplazará toda la base de datos con «${restoreFile?.name || 'la copia seleccionada'}». Esta acción no se puede deshacer.`}
  confirmLabel="Restaurar"
  danger={true}
  onCancel={() => (confirmRestore = false)}
  onConfirm={runRestore}
/>

<style>
  .section h2 { margin-top: 0; }
  .section h3 { font-size: 0.95rem; margin-bottom: var(--space-xs); }
  .muted { color: var(--text-muted); font-size: 0.86rem; }
  .actions { display: flex; gap: var(--space-sm); flex-wrap: wrap; margin-top: var(--space-md); }
  .divider { height: 1px; background: var(--border); margin: var(--space-lg) 0; }
  .details { display: grid; gap: 0; margin: var(--space-md) 0 0; }
  .details > div {
    display: flex;
    justify-content: space-between;
    gap: var(--space-sm);
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border-subtle);
  }
  .details dt { color: var(--text-muted); font-size: 0.85rem; }
  .details dd { margin: 0; font-size: 0.85rem; font-weight: 500; text-align: right; }
  .details dd.ok { color: var(--success); }
  .details dd.bad { color: var(--warning); }
  .issues { margin-top: var(--space-md); color: var(--warning); font-size: 0.85rem; }
  .issues ul { margin: var(--space-xs) 0 0; padding-left: 1.2rem; }
  .result { margin-top: var(--space-md); font-size: 0.85rem; }
  .result.ok { color: var(--success); }
</style>
