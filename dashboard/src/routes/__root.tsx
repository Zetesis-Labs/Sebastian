import type { ReactNode } from 'react'
import { HeadContent, Link, Outlet, Scripts, createRootRoute } from '@tanstack/react-router'
import '../styles.css'

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: 'utf-8' },
      { name: 'viewport', content: 'width=device-width, initial-scale=1' },
      { title: 'Sebastian Control Room' },
      {
        name: 'description',
        content: 'Panel de administración y grabaciones de Sebastian',
      },
    ],
  }),
  component: RootComponent,
  notFoundComponent: () => (
    <main className="page-shell empty-page">
      <p className="eyebrow">404 · Ruta desconocida</p>
      <h1>Esto no forma parte del repertorio.</h1>
      <Link to="/" className="button-link">Volver al panel</Link>
    </main>
  ),
})

function RootComponent() {
  return (
    <RootDocument>
      <header className="site-header">
        <Link to="/" className="brand" aria-label="Sebastian Control Room">
          <span className="brand-mark" aria-hidden="true"><i /><i /><i /></span>
          <span>
            <strong>Sebastian</strong>
            <small>Control room</small>
          </span>
        </Link>
        <div className="system-state"><span /> Sistema enlazado</div>
      </header>
      <Outlet />
    </RootDocument>
  )
}

function RootDocument({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <html lang="es">
      <head><HeadContent /></head>
      <body>
        {children}
        <Scripts />
      </body>
    </html>
  )
}
