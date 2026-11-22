<script>
  import {
    SYSTEM_FIELDS,
    buildApiEndpoints,
    documentStructure,
    sampleResponse,
  } from '../api-reference.js'
  import { notify } from '../stores.js'
  import Icon from './Icon.svelte'

  let {
    collection = '',
    type = 'base',
    fields = [],
    /** Callback opcional que ejecuta un GET real; debe devolver la respuesta JSON. */
    onProbe = null,
  } = $props()

  let lang = $state('curl')
  let probe = $state({ loading: false, error: '', response: '' })

  const baseURL =
    typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:8090'

  const hasUserFields = $derived(
    (fields || []).some((field) => field?.name && !SYSTEM_FIELDS.has(field.name))
  )
  const structure = $derived(documentStructure(fields, type))
  const responseJson = $derived(JSON.stringify(sampleResponse(fields, type), null, 2))
  const endpoints = $derived(buildApiEndpoints({ collection, type, baseURL, fields }))

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(String(text))
      notify('Copiado al portapapeles.', 'success')
    } catch {
      notify('No se pudo copiar al portapapeles.', 'error')
    }
  }

  async function probeApi() {
    if (!onProbe) return
    probe = { loading: true, error: '', response: '' }
    try {
      const data = await onProbe()
      probe = { loading: false, error: '', response: JSON.stringify(data, null, 2) }
    } catch (error) {
      probe = {
        loading: false,
        error: error instanceof Error ? error.message : String(error),
        response: '',
      }
    }
  }
</script>

<div class="stack api-tab">
  <div class="api-hero">
    <div>
      <h3>API REST de <code>{collection}</code></h3>
      <p class="muted tiny">Cualquier app (web, móvil o script) puede leer y escribir esta colección por HTTP. El panel usa esta misma API.</p>
    </div>
    <div class="row api-base-row">
      <code class="api-base-url">{baseURL}</code>
      <button type="button" class="btn btn-sm" onclick={() => copyText(baseURL)}>Copiar</button>
    </div>
  </div>

  <div class="callout callout-info">
    <span>
      Autenticación: envía <code>Authorization: Bearer &lt;token&gt;</code>.
      {#if type === 'auth'}
        Inicia sesión como usuario con <code>POST /api/collections/{collection}/auth-with-password</code>.
      {:else}
        Obtén un token de superusuario con <code>POST /api/admin/auth-with-password</code>.
      {/if}
    </span>
  </div>

  <details class="api-endpoint" open>
    <summary>
      <Icon name="code" size={14} />
      Estructura del documento
      <span class="spacer"></span>
      <span class="muted tiny">{structure.length} campos</span>
    </summary>
    <div class="stack mt-2">
      <p class="muted tiny">Campos que acepta la colección. Los del sistema (<code>id</code>, <code>created</code>, <code>updated</code>) los genera el servidor.</p>
      {#if !hasUserFields && type !== 'auth'}
        <div class="callout callout-info">
          <span>Esta colección no define campos, así que acepta documentos JSON libres. Puedes añadirlos en la pestaña <strong>Campos</strong>.</span>
        </div>
      {/if}
      <div class="table-wrap">
        <table class="api-params">
          <thead><tr><th>Campo</th><th>Tipo</th><th>Obligatorio</th><th>Notas</th></tr></thead>
          <tbody>
            {#each structure as row (row.name)}
              <tr>
                <td><code>{row.name}</code></td>
                <td class="muted">{row.type}</td>
                <td>{row.required ? 'Sí' : '—'}</td>
                <td class="muted">{row.note || '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <div class="row">
        <button type="button" class="btn btn-xs" onclick={() => copyText(responseJson)}>Copiar JSON de ejemplo</button>
      </div>
      <pre class="code-block">{responseJson}</pre>
    </div>
  </details>

  <div class="row">
    <div class="segmented" role="group" aria-label="Lenguaje de ejemplo">
      <button type="button" class="btn btn-xs" class:btn-primary={lang === 'curl'} onclick={() => (lang = 'curl')}>cURL</button>
      <button type="button" class="btn btn-xs" class:btn-primary={lang === 'js'} onclick={() => (lang = 'js')}>JavaScript</button>
    </div>
    <span class="spacer"></span>
    <span class="muted tiny">Ejemplos listos para copiar</span>
  </div>

  {#each endpoints as endpoint, index (endpoint.id)}
    {#if endpoint.group === 'auth' && (index === 0 || endpoints[index - 1].group !== 'auth')}
      <h3 class="mt-2">Inicio de sesión de usuarios</h3>
    {/if}
    <details class="api-endpoint" open={endpoint.open}>
      <summary>
        <span class="method method-{endpoint.method.toLowerCase()}">{endpoint.method}</span>
        <code>{endpoint.path}</code>
        <span class="spacer"></span>
        <span class="muted tiny">{endpoint.title}</span>
      </summary>
      <div class="stack mt-2">
        <p class="muted tiny">{endpoint.desc}</p>
        {#if endpoint.params}
          <div class="table-wrap">
            <table class="api-params">
              <thead><tr><th>Parámetro</th><th>Uso</th></tr></thead>
              <tbody>
                {#each endpoint.params as [name, help] (name)}
                  <tr><td><code>{name}</code></td><td class="muted">{help}</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
        <div class="row">
          <button type="button" class="btn btn-xs" onclick={() => copyText(endpoint.examples[lang])}>Copiar ejemplo</button>
          {#if endpoint.id === 'list' && onProbe}
            <button type="button" class="btn btn-xs btn-success-outline" onclick={probeApi} disabled={probe.loading}>
              {#if probe.loading}<span class="spinner" aria-hidden="true"></span>{/if}
              Probar ahora
            </button>
          {/if}
        </div>
        <pre class="code-block">{endpoint.examples[lang]}</pre>
        {#if endpoint.id === 'list' && probe.error}
          <div class="callout callout-danger">{probe.error}</div>
        {/if}
        {#if endpoint.id === 'list' && probe.response}
          <div class="stack">
            <p class="muted tiny">Respuesta real (limit=5):</p>
            <pre class="code-block api-response">{probe.response}</pre>
          </div>
        {/if}
      </div>
    </details>
  {/each}
</div>

<style>
  .api-tab { gap: 12px; }
  .api-hero { display: flex; flex-direction: column; gap: 10px; }
  .api-hero h3 { font-size: 15px; }
  .api-base-row { flex-wrap: wrap; }
  .api-base-url {
    font-family: var(--mono);
    font-size: 12.5px;
    padding: 6px 10px;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-subtle);
    word-break: break-all;
  }
  .segmented { display: inline-flex; gap: 4px; }
  .api-endpoint {
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    padding: 10px 12px;
    background: var(--bg-elevated);
  }
  .api-endpoint summary { cursor: pointer; font-weight: 600; font-size: 13px; }
  .api-endpoint summary { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .api-endpoint summary code { font-size: 12px; word-break: break-all; }
  .method {
    display: inline-block;
    min-width: 54px;
    text-align: center;
    font-family: var(--mono);
    font-size: 10.5px;
    font-weight: 700;
    letter-spacing: 0.04em;
    padding: 2px 6px;
    border-radius: 6px;
  }
  .method-get { color: #2563eb; background: rgba(37, 99, 235, 0.12); }
  .method-post { color: #15803d; background: rgba(21, 128, 61, 0.12); }
  .method-patch { color: #b45309; background: rgba(180, 83, 9, 0.12); }
  .method-put { color: #7c3aed; background: rgba(124, 58, 237, 0.12); }
  .method-delete { color: #b91c1c; background: rgba(185, 28, 28, 0.12); }
  .code-block {
    margin: 0;
    padding: 12px 14px;
    overflow-x: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    background: var(--bg-subtle);
    color: var(--text);
    font-family: var(--mono);
    font-size: 12px;
    line-height: 1.55;
    white-space: pre;
  }
  .api-response { max-height: 320px; overflow: auto; }
  .api-params th, .api-params td { font-size: 12px; padding: 4px 8px; }
</style>
