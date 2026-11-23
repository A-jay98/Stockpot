import { isRouteErrorResponse, Link, useRouteError } from 'react-router'
import { Button } from '../ui'

export function ErrorPage() {
  const error = useRouteError()
  const notFound = isRouteErrorResponse(error) && error.status === 404
  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-[420px] flex-col justify-center gap-4 px-4">
      <h1 className="t-title-lg m-0">{notFound ? 'Page not found' : 'Something went wrong'}</h1>
      <p className="t-body m-0 text-ink-muted">
        {notFound
          ? "That page doesn't exist."
          : 'Try again. If it keeps happening, tell the admin.'}
      </p>
      <Button asChild className="self-start">
        <Link to="/">Go home</Link>
      </Button>
    </main>
  )
}
