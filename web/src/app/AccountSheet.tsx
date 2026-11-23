import { Monitor, Moon, Shield, Sun } from 'lucide-react'
import type { ReactElement } from 'react'
import { Link } from 'react-router'
import { Button, Sheet, SheetContent, SheetTrigger, Tabs, TabsList, TabsTrigger } from '../ui'
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
          <Tabs value={theme} onValueChange={(v) => setTheme(v as ThemePreference)}>
            <TabsList aria-label="Theme">
              <TabsTrigger value="system">
                <Monitor className="size-4" aria-hidden />
                System
              </TabsTrigger>
              <TabsTrigger value="light">
                <Sun className="size-4" aria-hidden />
                Light
              </TabsTrigger>
              <TabsTrigger value="dark">
                <Moon className="size-4" aria-hidden />
                Dark
              </TabsTrigger>
            </TabsList>
          </Tabs>
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
