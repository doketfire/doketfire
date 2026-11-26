# Panel de administración de Doketfire

Aplicación Svelte 5 + Vite servida por el binario de Go bajo `/_/` y compilada dentro de `ui/build` (que `ui/embed.go` incrusta).

## Desarrollo

```bash
cd ui
bun install
bun run dev
```

El servidor de desarrollo usa `base: '/_/'`; en producción el binario Go sirve los archivos generados.

## Producción

```bash
cd ui
bun run build
```

Vite vacía `ui/build` y genera referencias con hash a `/_/assets/...` en `ui/build/index.html`. No edites `ui/build` a mano: es artefacto generado.

## Pruebas

```bash
cd ui
bun test
```

Cubren el cliente HTTP ( renovación de token deduplicada, mapeo de errores, URLs con cursor y abortado por clave ) y los formateadores.

## Notas de seguridad

- Los tokens JWT viven en `sessionStorage` con un espejo en memoria; nunca en URL ni `localStorage`.
- La CSP del servidor prohíbe estilos y scripts en línea, por lo que todo el CSS se emite en un archivo externo y no se usan `style:` ni `{@html}`.
- Las descargas usan `fetch` con `Authorization` y object URLs, sin exponer tokens en la URL.
