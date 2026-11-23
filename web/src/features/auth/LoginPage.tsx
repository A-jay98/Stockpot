import { Lockup } from '../../app/Logo'
import { Placeholder } from '../../app/Page'

/** Target of the API client's 401 redirect. Passkey sign-in arrives with m0-2. */
export function LoginPage() {
  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-[420px] flex-col justify-center gap-6 px-4">
      <Lockup />
      <h1 className="t-title-lg m-0">Sign in</h1>
      <Placeholder milestone="M0 (identity)">Sign in with a passkey.</Placeholder>
    </main>
  )
}
