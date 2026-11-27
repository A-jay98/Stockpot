import { createContext, type ReactNode, useContext } from 'react'

/** Extra header actions the app shell offers every page (the notification bell). */
export const ShellActionsContext = createContext<ReactNode>(null)

export const useShellActions = () => useContext(ShellActionsContext)
