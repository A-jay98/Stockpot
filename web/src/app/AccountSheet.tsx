import { Monitor, Moon, Shield, Sun } from 'lucide-react'
import type { ReactElement } from 'react'
import { Link } from 'react-router'
import { Button, Segmented, Sheet, SheetContent, SheetTrigger } from '../ui'
import { ServerStatus } from './ServerStatus'
import { type ThemePreference, useTheme } from './theme'

/** Account menu: theme, admin entry, server version. Profile and household arrive with m0-2. */
export function AccountSheet({ trigger }: { trigger: ReactElement }) {
  const [theme, setTheme] = useTheme()
  return (
    <Sheet>
      <SheetTrigger asChild>{trigger}</SheetTrigger>
      <SheetContent title="Account" description="Profile and household settings come with sign-in.">
        <div className="flex flex-col gap-2">
          <span className="t-label text-ink-muted">Theme</span>
          <Segmented<ThemePreference>
            aria-label="Theme"
            value={theme}
            onValueChange={setTheme}
            options={[
              {
                value: 'system',
                label: (
                  <>
                    <Monitor className="size-4" aria-hidden />
                    System
                  </>
                ),
              },
              {
                value: 'light',
                label: (
                  <>
                    <Sun className="size-4" aria-hidden />
                    Light
                  </>
                ),
              },
              {
                value: 'dark',
                label: (
                  <>
                    <Moon className="size-4" aria-hidden />
                    Dark
                  </>
                ),
              },
            ]}
          />
        </div>
        <Button variant="secondary" asChild>
          <Link to="/admin">
            <Shield aria-hidden />
            Admin panel
          </Link>
        </Button>
        <ServerStatus />
      </SheetContent>
    </Sheet>
  )
}
