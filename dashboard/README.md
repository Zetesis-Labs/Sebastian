# Dashboard de Sebastian

Panel SSR con React y TanStack Start. Consume el contrato OpenAPI de Go desde
funciones de servidor; el secreto administrativo no se incluye en el JavaScript
del navegador. El panel todavía no tiene login propio: su red/ingress determina
quién puede utilizarlo.

## Funcionalidad

- Catálogo `recordings`: resumen, listado y detalle de grabaciones registradas.
- Flota: descubrimiento/adopción, ficha, perfil, configuración deseada frente a
  ejecutada, eventos, rotación de secreto y olvido.
- Instalador embebido en `/installer/`, preparado con la organización y el control
  room. Los datos de aprovisionamiento sí llegan al navegador.
- Reuniones en `/meetings`: búsqueda, detalle, transcripción sincronizada,
  resumen, nombres de hablantes, conservar, reintentar, exportar y borrar.
  La ficha del dispositivo permite iniciar/parar y ajustar límites.

Reuniones B–F está en el PR #50 abierto. El reproductor obtiene el audio por
`fetch` y `blob:`; su prueba manual sigue pendiente según
[SESSION.md](../SESSION.md). Las reuniones tienen su propio recorrido y no crean
una fila duplicada en el catálogo `recordings`.

## Desarrollo

Dentro del devcontainer, instalar también las dependencias de `web-installer`
según [DEVCONTAINER.md](../docs/DEVCONTAINER.md). Desde `/workspace/dashboard`:

```bash
npm ci
npm run generate:api
npm run check
npm run dev
```

El panel escucha en `http://localhost:3001`. Necesita `SEBASTIAN_API_URL`
(configurado como `http://127.0.0.1:8787` en desarrollo) y
`SEBASTIAN_ADMIN_SECRET`, compartido con Go.

`src/lib/api-schema.ts` se genera desde `server/api/openapi.yaml` y se versiona.
CI verifica que no haya divergencias, pasa tipos y Vitest, construye el panel
con el instalador y audita dependencias. Las comprobaciones de reproducción,
WebSerial y recorrido visual no están cubiertas por esos tests lógicos.
