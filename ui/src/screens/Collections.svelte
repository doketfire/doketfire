<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { SYSTEM_FIELDS } from '../lib/api-reference.js'
  import { formatDate, formatNumber, truncate } from '../lib/format.js'
  import { notify, openCollection, createCollectionRequest } from '../lib/stores.js'
  import ApiReference from '../lib/components/ApiReference.svelte'
  import Icon from '../lib/components/Icon.svelte'
  import Modal from '../lib/components/Modal.svelte'
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte'

  const fieldTypes = ['string', 'number', 'bool', 'array', 'object', 'file', 'any']
  const ruleOps = [
    ['list', 'Ver lista'],
    ['view', 'Ver detalle'],
    ['create', 'Crear'],
    ['update', 'Editar'],
    ['delete', 'Eliminar'],
  ]

  const templates = [
    {
      id: 'blog',
      label: 'Artículos',
      hint: 'Título, contenido y estado',
      type: 'base',
      sampleName: 'articulos',
      fields: [
        { name: 'titulo', type: 'string', required: true },
        { name: 'contenido', type: 'string' },
        { name: 'publicado', type: 'bool' },
      ],
    },
    {
      id: 'productos',
      label: 'Productos',
      hint: 'Nombre, precio y stock',
      type: 'base',
      sampleName: 'productos',
      fields: [
        { name: 'nombre', type: 'string', required: true },
        { name: 'precio', type: 'number', required: true },
        { name: 'stock', type: 'number' },
      ],
    },
    {
      id: 'tareas',
      label: 'Tareas',
      hint: 'Título, hecha y prioridad',
      type: 'base',
      sampleName: 'tareas',
      fields: [
        { name: 'titulo', type: 'string', required: true },
        { name: 'hecha', type: 'bool' },
        { name: 'prioridad', type: 'number' },
      ],
    },
    {
      id: 'usuarios',
      label: 'Usuarios con acceso',
      hint: 'Cuentas con correo y contraseña',
      type: 'auth',
      sampleName: 'clientes',
      fields: [{ name: 'nombre', type: 'string' }],
    },
    {
      id: 'blanco',
      label: 'En blanco',
      hint: 'Sin campos, los añades después',
      type: 'base',
      sampleName: 'nueva_coleccion',
      fields: [],
    },
  ]

  let collections = $state([])
  let selected = $state('')
  let detail = $state(null)
  let loading = $state(false)
  let listError = $state('')
  let tab = $state('records')

  let records = $state([])
  let recordsLoading = $state(false)
  let recordsError = $state('')
  let query = $state({ filter: '', sort: '', limit: 25 })
  let cursor = $state('')
  let nextCursor = $state('')
  let cursorStack = $state([])

  let createOpen = $state(false)
  let createTemplate = $state('blog')
  let newCollection = $state({ name: '' })
  let creating = $state(false)

  let recordOpen = $state(false)
  let recordMode = $state('create')
  let recordId = $state('')
  let recordJson = $state('{}')
  let savingRecord = $state(false)
  let recordValues = $state({})
  let recordErrors = $state({})

  let deleteRecordTarget = $state(null)
  let deleteRecordOpen = $state(false)
  let deleteCollectionOpen = $state(false)

  let fieldDraft = $state([])
  let savingFields = $state(false)

  let accessDraft = $state({})
  let indexDraft = $state([])
  let cascadeDraft = $state([])
  let savingAdvanced = $state(false)

  let pendingOpen = ''

  const activeTemplate = $derived(templates.find((t) => t.id === createTemplate) || templates[0])

  const formFields = $derived(
    (detail?.fields || [])
      .filter((f) => !SYSTEM_FIELDS.has(f.name))
      .map((f) => ({ ...f, label: fieldLabel(f.name) }))
  )

  const visibleFields = $derived(
    (detail?.fields || []).filter((f) => !SYSTEM_FIELDS.has(f.name))
  )

  function fieldLabel(name) {
    return String(name || '')
      .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
      .replace(/[_-]+/g, ' ')
      .replace(/^\w/, (c) => c.toUpperCase())
  }

  function displayValue(value) {
    if (value === null || value === undefined) return '—'
    if (typeof value === 'object') return truncate(JSON.stringify(value), 40)
    return truncate(String(value), 40)
  }

  function slugify(raw) {
    let value = String(raw || '')
      .normalize('NFD')
      .replace(/[\u0300-\u036f]/g, '')
      .toLowerCase()
      .replace(/[^a-z0-9_]+/g, '_')
      .replace(/^_+|_+$/g, '')
      .replace(/_+/g, '_')
      .slice(0, 64)
    if (value && !/^[a-z]/.test(value)) value = `col_${value}`.slice(0, 64)
    return value
  }

  function defaultRules(type) {
    if (type === 'auth') {
      return {
        list: 'request.auth != null && resource.id == request.auth.id',
        view: 'request.auth != null && resource.id == request.auth.id',
        create: '',
        update: 'request.auth != null && resource.id == request.auth.id',
        delete: 'request.auth != null && resource.id == request.auth.id',
      }
    }
    return { list: 'request.auth != null', view: 'request.auth != null', create: 'request.auth != null', update: 'request.auth != null', delete: 'request.auth != null' }
  }

  function resetPagination() {
    cursor = ''
    nextCursor = ''
    cursorStack = []
  }

  function selectCollection(name) {
    selected = name
    resetPagination()
    return loadSelected()
  }

  async function loadCollections() {
    listError = ''
    try {
      const data = await api('/api/admin/collections', { key: 'collections-list' })
      collections = data?.items || []
      const current = collections.find((c) => c.name === selected)
      if (current && detail) detail = { ...detail, documentCount: current.documentCount }
      if (pendingOpen && collections.some((c) => c.name === pendingOpen)) {
        const name = pendingOpen
        pendingOpen = ''
        await selectCollection(name)
      } else if (!selected && collections.length) {
        await selectCollection(collections[0].name)
      } else if (selected && !collections.some((c) => c.name === selected)) {
        const name = collections[0]?.name || ''
        detail = null
        if (name) await selectCollection(name)
        else selected = ''
      }
    } catch (error) {
      listError = error instanceof Error ? error.message : String(error)
    }
  }

  async function loadSelected({ resetTab = true } = {}) {
    if (!selected) return
    loading = true
    try {
      detail = await api(`/api/admin/collections/${encodeURIComponent(selected)}`, {
        key: `col-detail-${selected}`,
      })
      if (resetTab) tab = 'records'
      resetDrafts()
      await loadRecords()
    } catch (error) {
      recordsError = error instanceof Error ? error.message : String(error)
      detail = null
    } finally {
      loading = false
    }
  }

  function resetDrafts() {
    fieldDraft = (detail?.fields || []).filter((f) => !SYSTEM_FIELDS.has(f.name)).map((f) => ({ ...f }))
    indexDraft = (detail?.indexes || []).map((i) => ({ ...i }))
    cascadeDraft = (detail?.cascade || []).map((c) => ({ ...c }))
    const access = {}
    for (const [op] of ruleOps) {
      const present = Object.prototype.hasOwnProperty.call(detail?.rules || {}, op)
      const expr = present ? String(detail.rules[op] || '') : ''
      if (!present) access[op] = { mode: 'blocked', expr: '' }
      else if (expr === '') access[op] = { mode: 'public', expr: '' }
      else if (expr === 'request.auth != null') access[op] = { mode: 'auth', expr: '' }
      else access[op] = { mode: 'custom', expr }
    }
    accessDraft = access
  }

  async function loadRecords() {
    if (!selected) return
    recordsLoading = true
    recordsError = ''
    try {
      const params = {}
      if (query.filter) params.filter = query.filter
      if (query.sort) params.sort = query.sort
      if (cursor) params.cursor = cursor
      params.limit = query.limit || 25
      const data = await api(`/api/admin/collections/${encodeURIComponent(selected)}/records`, {
        key: `records-${selected}`,
        params,
      })
      records = data?.items || []
      nextCursor = data?.nextCursor || ''
    } catch (error) {
      recordsError = error instanceof Error ? error.message : String(error)
    } finally {
      recordsLoading = false
    }
  }

  function searchRecords() {
    resetPagination()
    loadRecords()
  }

  function openCreate() {
    createTemplate = 'blog'
    newCollection = { name: slugify(templates[0].sampleName) }
    createOpen = true
  }

  function chooseTemplate(template) {
    const previous = templates.find((t) => t.id === createTemplate)
    createTemplate = template.id
    if (!newCollection.name || newCollection.name === slugify(previous?.sampleName || '')) {
      newCollection = { name: slugify(template.sampleName) }
    }
  }

  async function createCollection() {
    const name = slugify(newCollection.name)
    newCollection = { name }
    if (!name) {
      notify('Escribe un nombre para la colección.', 'error')
      return
    }
    creating = true
    try {
      const template = activeTemplate
      await api('/api/admin/collections', {
        method: 'POST',
        body: {
          name,
          type: template.type,
          fields: template.fields.map((f) => ({ ...f })),
          indexes: [],
          rules: defaultRules(template.type),
        },
      })
      createOpen = false
      pendingOpen = name
      await loadCollections()
      notify(`Colección "${name}" creada.`, 'success')
    } catch (error) {
      notify(error instanceof Error ? error.message : String(error), 'error')
    } finally {
      creating = false
    }
  }

  async function deleteCollection() {
    await api(`/api/admin/collections/${encodeURIComponent(selected)}?confirm=${encodeURIComponent(selected)}`, {
      method: 'DELETE',
    })
    selected = ''
    detail = null
    records = []
    resetPagination()
    await loadCollections()
    notify('Colección eliminada', 'success')
  }

  function openRecord(record = null) {
    recordMode = record ? 'edit' : 'create'
    recordId = record?.id || ''
    recordJson = JSON.stringify(record || {}, null, 2)
    syncFormFromJson(record || {})
    if (!record && detail?.type === 'auth') {
      recordValues = { ...recordValues, password: '' }
    }
    recordOpen = true
  }

  function syncFormFromJson(source) {
    const values = {}
    for (const field of formFields) {
      const raw = source[field.name]
      if (raw === undefined || raw === null) {
        values[field.name] = field.type === 'bool' ? false : field.type === 'object' ? '{}' : ''
      } else if (field.type === 'array') {
        values[field.name] = Array.isArray(raw) ? raw.join(', ') : String(raw)
      } else if (field.type === 'object') {
        values[field.name] = JSON.stringify(raw, null, 2)
      } else if (field.type === 'bool') {
        values[field.name] = Boolean(raw)
      } else {
        values[field.name] = String(raw)
      }
    }
    recordValues = values
    recordErrors = {}
  }

  function syncJsonFromForm() {
    const body = {}
    const errors = {}
    for (const field of formFields) {
      const value = recordValues[field.name]
      const empty = value === '' || value === undefined || value === null
      if (field.required && empty && field.type !== 'bool') {
        errors[field.name] = 'Campo obligatorio'
        continue
      }
      if (empty) continue
      switch (field.type) {
        case 'bool':
          body[field.name] = Boolean(value)
          break
        case 'number': {
          const n = Number(value)
          if (!Number.isFinite(n)) errors[field.name] = 'Número inválido'
          else body[field.name] = n
          break
        }
        case 'array':
          body[field.name] = String(value).split(',').map((i) => i.trim()).filter(Boolean)
          break
        case 'object':
          try {
            body[field.name] = JSON.parse(String(value))
          } catch {
            errors[field.name] = 'JSON inválido'
          }
          break
        default:
          body[field.name] = String(value)
      }
    }
    recordErrors = errors
    if (Object.keys(errors).length) return false
    recordJson = JSON.stringify(body, null, 2)
    return true
  }

  async function saveRecord() {
    if (formFields.length && !syncJsonFromForm()) {
      notify('Revisa los campos marcados.', 'error')
      return
    }
    let body
    try {
      body = formFields.length ? JSON.parse(recordJson) : JSON.parse(recordJson || '{}')
    } catch {
      notify('El contenido JSON no es válido.', 'error')
      return
    }
    if (!body || typeof body !== 'object' || Array.isArray(body)) {
      notify('El documento debe ser un objeto JSON.', 'error')
      return
    }
    if (recordMode === 'create' && detail?.type === 'auth') {
      const password = String(recordValues.password || '')
      if (password.length < 8) {
        notify('La contraseña debe tener al menos 8 caracteres.', 'error')
        return
      }
      body.password = password
    }
    for (const key of ['id', 'created', 'updated', 'passwordHash', 'tokenKey']) delete body[key]
    savingRecord = true
    try {
      const path = `/api/collections/${encodeURIComponent(selected)}/records`
      if (recordMode === 'create') {
        await api(path, { method: 'POST', body })
      } else {
        await api(`${path}/${encodeURIComponent(recordId)}`, { method: 'PATCH', body })
      }
      recordOpen = false
      if (recordMode === 'create') {
        resetPagination()
      }
      await loadRecords()
      await loadCollections()
      notify(recordMode === 'create' ? 'Documento creado' : 'Documento actualizado', 'success')
    } catch (error) {
      notify(error instanceof Error ? error.message : String(error), 'error')
    } finally {
      savingRecord = false
    }
  }

  async function deleteRecord() {
    const id = deleteRecordTarget?.id
    if (!id) return
    await api(`/api/collections/${encodeURIComponent(selected)}/records/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    })
    deleteRecordTarget = null
    deleteRecordOpen = false
    await loadRecords()
    await loadCollections()
    notify('Documento eliminado', 'success')
  }

  async function saveFields() {
    savingFields = true
    try {
      const fields = fieldDraft
        .filter((f) => f.name?.trim())
        .map((f) => ({
          name: f.name.trim(),
          type: f.type || 'string',
          ...(f.required ? { required: true } : {}),
          ...(f.maxLen ? { maxLen: Number(f.maxLen) } : {}),
        }))
      await api(`/api/admin/collections/${encodeURIComponent(selected)}`, { method: 'PATCH', body: { fields } })
      notify('Campos guardados.', 'success')
      await loadSelected({ resetTab: false })
      await loadCollections()
    } catch (error) {
      notify(error instanceof Error ? error.message : String(error), 'error')
    } finally {
      savingFields = false
    }
  }

  async function saveAdvanced() {
    for (const [op, label] of ruleOps) {
      const entry = accessDraft[op] || { mode: 'blocked', expr: '' }
      if (entry.mode === 'custom' && !String(entry.expr || '').trim()) {
        notify(`La regla "${label}" está en modo personalizado pero está vacía.`, 'error')
        return
      }
    }
    savingAdvanced = true
    try {
      const rules = {}
      for (const [op] of ruleOps) {
        const entry = accessDraft[op] || { mode: 'blocked', expr: '' }
        if (entry.mode === 'blocked') rules[op] = null
        else if (entry.mode === 'auth') rules[op] = 'request.auth != null'
        else if (entry.mode === 'public') rules[op] = ''
        else rules[op] = entry.expr
      }
      const indexes = indexDraft
        .filter((i) => i.name?.trim() && i.field?.trim())
        .map((i) => ({
          name: i.name.trim(),
          field: i.field.trim(),
          ...(i.unique ? { unique: true } : {}),
          ...(i.fulltext ? { fulltext: true } : {}),
          ...(i.state ? { state: i.state } : {}),
        }))
      const cascade = cascadeDraft
        .filter((c) => c.collection?.trim() && c.field?.trim())
        .map((c) => ({ collection: c.collection.trim(), field: c.field.trim() }))
      await api(`/api/admin/collections/${encodeURIComponent(selected)}`, {
        method: 'PATCH',
        body: { rules, indexes, cascade },
      })
      notify('Configuración avanzada guardada.', 'success')
      await loadSelected({ resetTab: false })
      await loadCollections()
    } catch (error) {
      notify(error instanceof Error ? error.message : String(error), 'error')
    } finally {
      savingAdvanced = false
    }
  }

  function nextPage() {
    if (!nextCursor) return
    cursorStack = [...cursorStack, cursor]
    cursor = nextCursor
    loadRecords()
  }

  function prevPage() {
    if (!cursorStack.length) return
    const stack = cursorStack.slice(0, -1)
    cursor = cursorStack[cursorStack.length - 1] || ''
    cursorStack = stack
    loadRecords()
  }

  onMount(() => {
    const unsubscribeOpen = openCollection.subscribe((name) => {
      if (!name) return
      pendingOpen = name
      openCollection.set('')
      if (collections.length) {
        pendingOpen = ''
        selectCollection(name)
      }
    })
    const unsubscribeCreate = createCollectionRequest.subscribe((n) => {
      if (!n) return
      createCollectionRequest.set(0)
      openCreate()
    })
    loadCollections()
    return () => {
      unsubscribeOpen()
      unsubscribeCreate()
    }
  })
</script>

<div class="stack">
  <div class="page-head">
    <div class="page-title">
      <h1>Colecciones</h1>
      <p class="page-subtitle">Crea una colección, define sus campos y añade documentos.</p>
    </div>
    <div class="page-actions">
      <button type="button" class="btn btn-primary" onclick={openCreate}>
        <Icon name="plus" size={14} /> Nueva colección
      </button>
    </div>
  </div>

  {#if listError}
    <div class="callout callout-danger">{listError}</div>
  {/if}

  <div class="split">
    <aside class="card side-panel">
      <div class="row mb-2">
        <h2>Colecciones</h2>
        <span class="spacer"></span>
        <span class="badge">{collections.length}</span>
      </div>
      <div class="stack list-stack">
        {#each collections as col (col.name)}
          <button
            type="button"
            class="btn btn-ghost btn-block collection-btn"
            class:btn-subtle={selected === col.name}
            onclick={() => selectCollection(col.name)}
          >
            <Icon name={col.type === 'auth' ? 'users' : 'database'} size={14} />
            <span class="truncate">{col.name}</span>
            <span class="spacer"></span>
            <span class="badge">{col.documentCount}</span>
          </button>
        {:else}
          <p class="muted tiny center">No hay colecciones todavía.</p>
        {/each}
      </div>
    </aside>

    <section class="card detail-panel">
      {#if loading && !detail}
        <p class="center muted">Cargando…</p>
      {:else if !detail}
        <div class="welcome center">
          <div class="welcome-icon" aria-hidden="true"><Icon name="database" size={28} /></div>
          <h2>{collections.length ? 'Selecciona una colección' : 'Crea tu primera colección'}</h2>
          <p class="muted">
            {collections.length
              ? 'Elige una de la lista para ver sus documentos.'
              : 'Una colección es como una carpeta para tus datos. Elige una plantilla y ponle nombre.'}
          </p>
          <button type="button" class="btn btn-primary" onclick={openCreate}>
            <Icon name="plus" size={15} /> {collections.length ? 'Nueva colección' : 'Crear colección'}
          </button>
        </div>
      {:else}
        <div class="page-head">
          <div class="page-title">
            <h2>{detail.name}</h2>
            <div class="badge-row">
              <span class="badge badge-info">{detail.type === 'auth' ? 'Usuarios' : 'Datos'}</span>
              <span class="badge">{formatNumber(detail.documentCount)} documentos</span>
            </div>
          </div>
          <div class="page-actions">
            <button type="button" class="btn btn-sm btn-success-outline" onclick={() => openRecord()}>
              <Icon name="plus" size={13} /> {detail.type === 'auth' ? 'Nuevo usuario' : 'Nuevo documento'}
            </button>
            <button type="button" class="btn btn-sm btn-danger" onclick={() => (deleteCollectionOpen = true)}>
              <Icon name="trash" size={13} /> Eliminar
            </button>
          </div>
        </div>

        <div class="tabs" role="tablist" aria-label="Secciones de la colección">
          <button type="button" role="tab" class="tab" class:active={tab === 'records'} aria-selected={tab === 'records'} onclick={() => (tab = 'records')}>Registros</button>
          <button type="button" role="tab" class="tab" class:active={tab === 'fields'} aria-selected={tab === 'fields'} onclick={() => (tab = 'fields')}>Campos</button>
          <button type="button" role="tab" class="tab" class:active={tab === 'api'} aria-selected={tab === 'api'} onclick={() => (tab = 'api')}>API</button>
          <button type="button" role="tab" class="tab" class:active={tab === 'advanced'} aria-selected={tab === 'advanced'} onclick={() => (tab = 'advanced')}>Avanzado</button>
        </div>

        <div class="mt-3">
          {#if tab === 'records'}
            <div class="stack">
              <div class="row">
                <div class="search-input" style="flex: 1; min-width: 200px;">
                  <Icon name="search" size={14} />
                  <input
                    class="input"
                    type="search"
                    placeholder='Filtrar, p. ej. titulo = "Hola"'
                    bind:value={query.filter}
                    aria-label="Filtrar documentos"
                    onkeydown={(e) => e.key === 'Enter' && searchRecords()}
                  />
                </div>
                <input
                  class="input"
                  type="text"
                  placeholder="Orden (ej. -created)"
                  bind:value={query.sort}
                  aria-label="Orden"
                  onkeydown={(e) => e.key === 'Enter' && searchRecords()}
                  style="max-width: 170px;"
                />
                <input
                  class="input"
                  type="number"
                  min="1"
                  max="100"
                  bind:value={query.limit}
                  aria-label="Límite"
                  onchange={searchRecords}
                  style="max-width: 90px;"
                />
                <button type="button" class="btn btn-sm" onclick={searchRecords}>Buscar</button>
              </div>

              {#if recordsError}
                <div class="callout callout-danger">{recordsError}</div>
              {:else if recordsLoading}
                <p class="center muted">Cargando documentos…</p>
              {:else if !records.length}
                <div class="center stack empty-records">
                  <p class="muted">No hay documentos todavía.</p>
                  <button type="button" class="btn btn-primary" onclick={() => openRecord()}>
                    <Icon name="plus" size={14} /> Añadir el primero
                  </button>
                </div>
              {:else}
                <div class="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>ID</th>
                        {#each visibleFields as field (field.name)}
                          <th>{fieldLabel(field.name)}</th>
                        {/each}
                        <th>Actualizado</th>
                        <th>Acciones</th>
                      </tr>
                    </thead>
                    <tbody>
                      {#each records as record (record.id)}
                        <tr>
                          <td class="id-pill" title={record.id}>{truncate(record.id, 10)}</td>
                          {#each visibleFields as field (field.name)}
                            <td class="truncate">{displayValue(record[field.name])}</td>
                          {/each}
                          <td class="nowrap tiny muted">{formatDate(record.updated || record.created)}</td>
                          <td>
                            <div class="row-actions">
                              <button type="button" class="btn btn-xs" onclick={() => openRecord(record)}>Editar</button>
                              <button type="button" class="btn btn-xs btn-danger" onclick={() => { deleteRecordTarget = record; deleteRecordOpen = true }}>Borrar</button>
                            </div>
                          </td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
                <div class="row">
                  <button type="button" class="btn btn-sm" onclick={prevPage} disabled={cursorStack.length === 0}>← Anterior</button>
                  <button type="button" class="btn btn-sm" onclick={nextPage} disabled={!nextCursor}>Siguiente →</button>
                  <span class="spacer"></span>
                  <span class="muted tiny">{records.length} en esta página</span>
                </div>
              {/if}
            </div>

          {:else if tab === 'fields'}
            <div class="stack">
              <p class="muted tiny">Los campos definen el formulario al crear documentos. Los cambios se aplican al instante.</p>
              {#each fieldDraft as field, index (index)}
                <div class="row field-row">
                  <input class="input" type="text" bind:value={field.name} placeholder="nombre" aria-label="Nombre del campo" />
                  <select class="select" bind:value={field.type} aria-label="Tipo">
                    {#each fieldTypes as type (type)}<option value={type}>{type}</option>{/each}
                  </select>
                  <label class="checkbox-row"><input type="checkbox" bind:checked={field.required} /> obligatorio</label>
                  <button type="button" class="btn btn-xs btn-danger" onclick={() => (fieldDraft = fieldDraft.filter((_, i) => i !== index))}>Quitar</button>
                </div>
              {:else}
                <p class="muted tiny">Sin campos. Añade el primero.</p>
              {/each}
              <div class="row">
                <button type="button" class="btn btn-sm" onclick={() => (fieldDraft = [...fieldDraft, { name: '', type: 'string', required: false }])}>
                  <Icon name="plus" size={13} /> Añadir campo
                </button>
                <span class="spacer"></span>
                <button type="button" class="btn btn-sm btn-primary" onclick={saveFields} disabled={savingFields}>
                  {#if savingFields}<span class="spinner" aria-hidden="true"></span>{/if}
                  Guardar campos
                </button>
              </div>
            </div>

          {:else if tab === 'api'}
            <ApiReference
              collection={detail.name}
              type={detail.type}
              fields={detail.fields || []}
              onProbe={() =>
                api(`/api/collections/${encodeURIComponent(detail.name)}/records`, {
                  params: { limit: 5 },
                })}
            />
          {:else if tab === 'advanced'}
            <div class="stack">
              <details class="adv-box" open>
                <summary>Acceso desde tu aplicación</summary>
                <div class="stack mt-2">
                  <p class="muted tiny">Quién puede hacer cada operación. Los superusuarios siempre tienen acceso.</p>
                  {#each ruleOps as [op, label] (op)}
                    <div class="row access-row">
                      <span class="access-label">{label}</span>
                      <select class="select" bind:value={accessDraft[op].mode} aria-label={`Acceso ${label}`}>
                        <option value="blocked">Solo superusuarios</option>
                        <option value="auth">Usuarios con sesión</option>
                        <option value="public">Público</option>
                        <option value="custom">Personalizado</option>
                      </select>
                      {#if accessDraft[op].mode === 'custom'}
                        <input class="input mono" type="text" bind:value={accessDraft[op].expr} placeholder="expresión CEL" aria-label={`Regla ${label}`} />
                      {/if}
                    </div>
                  {/each}
                </div>
              </details>

              <details class="adv-box">
                <summary>Índices</summary>
                <div class="stack mt-2">
                  <p class="muted tiny">Aceleran las consultas y permiten valores únicos.</p>
                  {#each indexDraft as idx, index (index)}
                    <div class="row">
                      <input class="input" type="text" bind:value={idx.name} placeholder="nombre" aria-label="Nombre del índice" />
                      <input class="input" type="text" bind:value={idx.field} placeholder="campo" aria-label="Campo indexado" />
                      <label class="checkbox-row"><input type="checkbox" bind:checked={idx.unique} /> único</label>
                      <label class="checkbox-row"><input type="checkbox" bind:checked={idx.fulltext} /> texto</label>
                      <button type="button" class="btn btn-xs btn-danger" onclick={() => (indexDraft = indexDraft.filter((_, i) => i !== index))}>Quitar</button>
                    </div>
                  {:else}
                    <p class="muted tiny">Sin índices.</p>
                  {/each}
                  <button type="button" class="btn btn-sm" onclick={() => (indexDraft = [...indexDraft, { name: '', field: '', unique: false, fulltext: false }])}>
                    <Icon name="plus" size={13} /> Añadir índice
                  </button>
                </div>
              </details>

              <details class="adv-box">
                <summary>Borrado en cascada</summary>
                <div class="stack mt-2">
                  <p class="muted tiny">Al borrar un documento, borra también los que lo referencian.</p>
                  {#each cascadeDraft as item, index (index)}
                    <div class="row">
                      <input class="input" type="text" bind:value={item.collection} placeholder="colección hija" aria-label="Colección hija" />
                      <input class="input" type="text" bind:value={item.field} placeholder="campo de referencia" aria-label="Campo de referencia" />
                      <button type="button" class="btn btn-xs btn-danger" onclick={() => (cascadeDraft = cascadeDraft.filter((_, i) => i !== index))}>Quitar</button>
                    </div>
                  {:else}
                    <p class="muted tiny">Sin reglas de cascada.</p>
                  {/each}
                  <button type="button" class="btn btn-sm" onclick={() => (cascadeDraft = [...cascadeDraft, { collection: '', field: '' }])}>
                    <Icon name="plus" size={13} /> Añadir regla
                  </button>
                </div>
              </details>

              <div class="row">
                <span class="spacer"></span>
                <button type="button" class="btn btn-primary" onclick={saveAdvanced} disabled={savingAdvanced}>
                  {#if savingAdvanced}<span class="spinner" aria-hidden="true"></span>{/if}
                  Guardar avanzado
                </button>
              </div>
            </div>
          {/if}
        </div>
      {/if}
    </section>
  </div>
</div>

<Modal bind:open={createOpen} wide title="Nueva colección" subtitle="Elige una plantilla y ponle un nombre">
  <form class="form-stack" onsubmit={(e) => { e.preventDefault(); createCollection() }}>
    <div class="template-grid" role="group" aria-label="Plantillas">
      {#each templates as template (template.id)}
        <button
          type="button"
          class="template-card"
          class:selected={createTemplate === template.id}
          aria-pressed={createTemplate === template.id}
          onclick={() => chooseTemplate(template)}
        >
          <strong>{template.label}</strong>
          <small class="muted">{template.hint}</small>
        </button>
      {/each}
    </div>
    <div class="field">
      <label class="field-label" for="col-name">Nombre</label>
      <input
        id="col-name"
        class="input"
        value={newCollection.name}
        oninput={(e) => (newCollection = { name: slugify(e.currentTarget.value) })}
        placeholder="articulos"
        autocomplete="off"
        required
      />
      {#if newCollection.name}<span class="field-hint">Se guardará como <code>{newCollection.name}</code></span>{/if}
    </div>
    <div class="row">
      <span class="spacer"></span>
      <button type="submit" class="btn btn-primary" disabled={creating || !newCollection.name}>
        {#if creating}<span class="spinner" aria-hidden="true"></span>{/if}
        Crear colección
      </button>
    </div>
  </form>
</Modal>

<Modal bind:open={recordOpen} title={recordMode === 'create' ? (detail?.type === 'auth' ? 'Nuevo usuario' : 'Nuevo documento') : 'Editar documento'} subtitle={recordMode === 'edit' ? `ID ${recordId}` : detail?.name} wide>
  <form class="form-stack" onsubmit={(e) => { e.preventDefault(); saveRecord() }}>
    {#if formFields.length}
      <p class="muted tiny">Los campos con <span class="required-mark">*</span> son obligatorios.</p>
      <div class="form-grid">
        {#if detail?.type === 'auth' && recordMode === 'create'}
          <div class="field field-full">
            <label class="field-label" for="rec-password">Contraseña <span class="required-mark">*</span></label>
            <input id="rec-password" class="input" type="password" autocomplete="new-password" bind:value={recordValues.password} />
            <span class="field-hint">Mínimo 8 caracteres.</span>
          </div>
        {/if}
        {#each formFields as field (field.name)}
          <div class="field" class:field-full={field.type === 'object'}>
            <label class="field-label" for={`rec-${field.name}`}>
              {field.label}
              {#if field.required}<span class="required-mark">*</span>{/if}
            </label>
            {#if field.type === 'bool'}
              <label class="checkbox-row" for={`rec-${field.name}`}>
                <input id={`rec-${field.name}`} type="checkbox" bind:checked={recordValues[field.name]} />
                <span>Sí</span>
              </label>
            {:else if field.type === 'object'}
              <textarea id={`rec-${field.name}`} class="textarea code-input" rows={4} bind:value={recordValues[field.name]} placeholder="Objeto JSON, por ejemplo con clave y valor"></textarea>
            {:else}
              <input
                id={`rec-${field.name}`}
                class="input"
                type={field.type === 'number' ? 'number' : field.type === 'password' ? 'password' : 'text'}
                bind:value={recordValues[field.name]}
                placeholder={field.type === 'array' ? 'Uno, dos, tres' : field.type === 'number' ? '0' : ''}
              />
              {#if field.type === 'array'}<span class="field-hint">Separa los valores con comas.</span>{/if}
            {/if}
            {#if recordErrors[field.name]}<p class="field-error">{recordErrors[field.name]}</p>{/if}
          </div>
        {/each}
      </div>
      {#if detail?.type === 'auth' && recordMode === 'edit'}
        <div class="callout callout-info">
          <span>Para cambiar la contraseña, usa la sección <strong>Usuarios</strong>.</span>
        </div>
      {/if}
    {:else}
      <div class="callout callout-info">
        <span>Esta colección no tiene campos. Puedes guardar un JSON libre, o definirlos en la pestaña <strong>Campos</strong>.</span>
      </div>
      <textarea class="textarea code-input" rows={10} bind:value={recordJson} placeholder="Contenido JSON del documento"></textarea>
    {/if}
    <div class="row">
      <button type="button" class="btn" onclick={() => (recordOpen = false)}>Cancelar</button>
      <span class="spacer"></span>
      <button type="submit" class="btn btn-primary" disabled={savingRecord}>
        {#if savingRecord}<span class="spinner" aria-hidden="true"></span>{/if}
        Guardar
      </button>
    </div>
  </form>
</Modal>

<ConfirmDialog
  bind:open={deleteCollectionOpen}
  title="Eliminar colección"
  message={`¿Eliminar "${selected}" y todos sus documentos? Esta acción no se puede deshacer.`}
  confirmLabel="Eliminar colección"
  danger={true}
  requireText={selected}
  onConfirm={deleteCollection}
/>

<ConfirmDialog
  bind:open={deleteRecordOpen}
  title="Eliminar documento"
  message={`¿Eliminar definitivamente el documento ${deleteRecordTarget?.id || ''}?`}
  confirmLabel="Eliminar documento"
  danger={true}
  onConfirm={deleteRecord}
  onCancel={() => { deleteRecordTarget = null; deleteRecordOpen = false }}
/>

<style>
  .list-stack { gap: 4px; }
  .collection-btn { justify-content: flex-start; text-align: left; }
  .empty-records { padding: 28px 12px; }
  .welcome {
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    gap: 10px; padding: 48px 24px; min-height: 300px;
  }
  .welcome h2 { font-size: 20px; }
  .welcome p { max-width: 460px; line-height: 1.6; }
  .welcome-icon {
    display: grid; place-items: center; width: 56px; height: 56px;
    border-radius: 16px; background: var(--brand-soft); color: var(--brand);
  }
  .template-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
  .template-card {
    display: flex; flex-direction: column; gap: 3px; padding: 12px;
    text-align: left; border: 1px solid var(--border); border-radius: var(--radius);
    background: var(--bg-elevated); color: var(--text); cursor: pointer;
  }
  .template-card:hover { border-color: var(--brand-border); background: var(--bg-subtle); }
  .template-card.selected { border-color: var(--brand); background: var(--brand-soft); box-shadow: inset 0 0 0 1px var(--brand); }
  .template-card strong { font-size: 13px; }
  .template-card small { font-size: 11px; line-height: 1.4; }
  .field-row .input { flex: 1; min-width: 120px; }
  .field-row .select { max-width: 130px; }
  .required-mark { color: var(--danger); font-weight: 700; }
  .adv-box { border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 10px 12px; background: var(--bg-elevated); }
  .adv-box summary { cursor: pointer; font-weight: 600; font-size: 13px; }
  .access-row { align-items: center; }
  .access-label { width: 96px; flex: 0 0 96px; font-size: 13px; color: var(--text-muted); }
  .access-row .select { max-width: 190px; }
  @media (max-width: 640px) {
    .template-grid { grid-template-columns: minmax(0, 1fr); }
    .field-row, .access-row { flex-direction: column; align-items: stretch; }
    .access-label { width: auto; }
  }
</style>