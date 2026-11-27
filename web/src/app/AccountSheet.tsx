import { LogOut, Monitor, Moon, Settings, Shield, Sun } from 'lucide-react'
import type { ReactElement } from 'react'
import { Link } from 'react-router'
import { useMe, useSignOut, useSwitchHousehold } from '../features/auth/session'
import { Button, Segmented, Sheet, SheetClose, SheetContent, SheetTrigger, Switch } from '../ui'
import { FoodIcon, useColourfulIcons } from '../ui/food-icons'
import { ServerStatus } from './ServerStatus'
import { type ThemePreference, useTheme } from './theme'

/** Account menu: who is signed in, household switch, theme, admin entry, sign out, server version. */
export function AccountSheet({ trigger }: { trigger: ReactElement }) {
  const [theme, setTheme] = useTheme()
  const [colourful, setColourful] = useColourfulIcons()
  const me = useMe().data
  const switchHousehold = useSwitchHousehold()
  const signOut = useSignOut()
  const households = me?.households ?? []
  return (
    <Sheet>
      <SheetTrigger asChild>{trigger}</SheetTrigger>
      <SheetContent
        title={me?.user.displayName ?? 'Account'}
        description={me?.user.systemRole === 'admin' ? 'Admin' : 'Signed in'}
      >
        {households.length > 1 && (
          <div className="flex flex-col gap-2">
            <span className="t-label text-ink-muted">Household</span>
            <Segmented<string>
              aria-label="Household"
              value={me?.activeHouseholdId ?? ''}
              onValueChange={(id) => switchHousehold.mutate(id)}
              options={households.map((h) => ({ value: h.householdId, label: h.name }))}
            />
          </div>
        )}
        {households.length === 1 && (
          <p className="t-body-sm m-0 text-ink-muted">Household: {households[0]?.name}</p>
        )}
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
        <SheetClose asChild>
          <Button variant="secondary" asChild>
            <Link to="/settings">
              <Settings aria-hidden />
              Settings
            </Link>
          </Button>
        </SheetClose>
        <div className="flex items-center justify-between gap-3">
          <label htmlFor="colourful-icons" className="flex items-center gap-2">
            <FoodIcon k="tangerine" size={20} />
            <span className="t-label text-ink-muted">Colourful food icons</span>
          </label>
          <Switch id="colourful-icons" checked={colourful} onCheckedChange={setColourful} />
        </div>
        {me?.user.systemRole === 'admin' && (
          <Button variant="secondary" asChild>
            <Link to="/admin">
              <Shield aria-hidden />
              Admin panel
            </Link>
          </Button>
        )}
        <Button variant="ghost" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
          <LogOut aria-hidden />
          Sign out
        </Button>
        <ServerStatus />
        <a
          href="/licenses.txt"
          target="_blank"
          rel="noreferrer"
          className="t-body-sm text-ink-muted underline"
        >
          Open-source licences
        </a>
      </SheetContent>
    </Sheet>
  )
}
