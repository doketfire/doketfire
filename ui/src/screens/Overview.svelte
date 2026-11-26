<script>
  import { onMount, createEventDispatcher } from 'svelte'
  import { api } from '../lib/api.js'
  import { formatNumber } from '../lib/format.js'
  import { openCollection, createCollectionRequest } from '../lib/stores.js'
  import Icon from '../lib/components/Icon.svelte'

  const dispatch = createEventDispatcher()

  let collections = $state([])
  let loading = $state(true)
  let error = $state('')

  const totalDocuments = $derived(
    collections.reduce((sum, c) => sum + (Number(c.documentCount) || 0), 0)
  )

  async function load() {
    loading = true
    error = ''
    try {
      const data = await api('/api/admin/collections', { key: 'overview-collections' })
      collections = data?.items || []
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    } finally {
      loading = false
    }
  }

  function openCol(name) {
    openCollection.set(name)
    dispatch('open-collection')
  }

  function createCol() {
    createCollectionRequest.update((n) => n + 1)
    dispatch('create-collection')
  }

  onMount(load)
</script>

<div class="stack">
  <div class="page-head">
    <div class="page-title">
      <h1>Bienvenido a Doketfire</h1>
      <p class="page-subtitle">Tus datos organizados en colecciones. Empieza por crear una.</p>
    </div>
    <div class="page-actions">
      <button type="button" class="btn btn-primary" onclick={createCol}>
        <Icon name="plus" size={14} /> Nueva colección
      </button>
    </div>
  </div>

  {#if error}
    <div class="callout callout-danger">{error}</div>
  {:else if loading}
    <p class="center muted">Cargando…</p>
  {:else if !collections.length}
    <div class="welcome-stack">
      <div class="welcome-icon" aria-hidden="true"><Icon name="database" size={40} /></div>
      <h2>Empieza con una colección</h2>
      <p class="muted">Una colección es como una carpeta para tus datos. Crea la primera y empieza a guardar documentos.</p>
      <button type="button" class="btn btn-primary btn-block" onclick={createCol}>
        <Icon name="plus" size={15} /> Crear mi primera colección
      </button>
    </div>
  {:else}
    <div class="grid grid-2">
      <div class="metric-card">
        <span class="metric-label">Colecciones</span>
        <span class="metric-value">{collections.length}</span>
        <span class="metric-note">{collections.length === 1 ? 'colección' : 'colecciones'}</span>
      </div>
      <div class="metric-card">
        <span class="metric-label">Documentos totales</span>
        <span class="metric-value">{formatNumber(totalDocuments)}</span>
        <span class="metric-note">En todas las colecciones</span>
      </div>
    </div>

    <div class="page-head mt-4">
      <div class="page-title">
        <h2>Tus colecciones</h2>
        <p class="page-subtitle">Haz clic en una para ver y editar sus documentos.</p>
      </div>
    </div>

    <div class="card">
      <div class="card-body">
        <div class="collection-list">
          {#each collections as col (col.name)}
            <button type="button" class="collection-row" onclick={() => openCol(col.name)}>
              <div class="collection-info">
                <strong>{col.name}</strong>
                <p class="tiny muted">{formatNumber(col.documentCount)} documento{col.documentCount !== 1 ? 's' : ''}</p>
              </div>
              <Icon name="chevron" size={14} />
            </button>
          {/each}
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .welcome-stack {
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    gap: 10px; text-align: center; padding: 48px 24px;
  }
  .welcome-icon {
    display: grid; place-items: center; width: 64px; height: 64px;
    border-radius: 16px; background: var(--brand-soft); color: var(--brand); margin-bottom: 6px;
  }
  .welcome-stack h2 { font-size: 22px; }
  .welcome-stack p { max-width: 420px; line-height: 1.6; }
  .welcome-stack .btn { max-width: 340px; }
  .collection-list { display: flex; flex-direction: column; gap: 8px; }
  .collection-row {
    display: flex; align-items: center; justify-content: space-between; gap: 10px;
    width: 100%; padding: 12px 16px; text-align: left;
    border: 1px solid var(--border); border-radius: var(--radius-sm);
    background: var(--bg-elevated); color: var(--text); cursor: pointer;
  }
  .collection-row:hover { border-color: var(--brand); background: var(--bg-hover); }
  .collection-info strong { display: block; font-size: 14px; }
  .collection-info .tiny { margin-top: 2px; }

  @media (max-width: 640px) {
    .grid-2 { grid-template-columns: minmax(0, 1fr); }
  }
</style>