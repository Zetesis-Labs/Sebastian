# Sebastian Dashboard

Panel de administración SSR construido con React y TanStack Start. Consume el
OpenAPI de `sebastian-server` desde funciones de servidor: el secreto temporal
de administración nunca se incluye en el JavaScript del navegador.

Dentro del devcontainer:

```bash
cd /workspace/dashboard
npm ci
npm run generate:api
npm run check
npm run dev
```

El panel escucha en `http://localhost:3001`. Necesita:

- `SEBASTIAN_API_URL`, por defecto configurado por el devcontainer como
  `http://127.0.0.1:8787`.
- `SEBASTIAN_ADMIN_SECRET`, el mismo valor configurado en la API Go.

`src/lib/api-schema.ts` se genera desde `server/api/openapi.yaml` y se versiona
para permitir builds reproducibles del contenedor. CI lo regenera y comprueba
que no haya divergencias.
