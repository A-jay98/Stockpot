import { type ClassValue, clsx } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// Teach tailwind-merge the token-based scales from styles/app.css so conflicting
// classes (e.g. `shadow-card shadow-sheet`) merge correctly.
const twMerge = extendTailwindMerge({
  extend: {
    theme: {
      radius: ['sm', 'md', 'lg', 'xl', 'full'],
      shadow: ['card', 'sheet'],
      font: ['display', 'sans', 'mono'],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
