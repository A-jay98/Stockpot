import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { VitePWA } from 'vite-plugin-pwa'
import { defineConfig } from 'vitest/config'

// Brand colours for the manifest; keep in sync with src/styles/tokens.css (--brand, --bg).
const themeColor = '#2344A6'
const backgroundColor = '#F4F6F9'

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    VitePWA({
      // A new release waits until the user accepts the update prompt (src/pwa/UpdatePrompt.tsx).
      registerType: 'prompt',
      injectRegister: false,
      manifest: {
        id: '/',
        name: 'Stockpot',
        short_name: 'Stockpot',
        description: "Your household's fridge, pantry, shopping and recipes.",
        start_url: '/',
        scope: '/',
        display: 'standalone',
        theme_color: themeColor,
        background_color: backgroundColor,
        icons: [
          { src: '/icons/icon-192.png', sizes: '192x192', type: 'image/png', purpose: 'any' },
          { src: '/icons/icon-512.png', sizes: '512x512', type: 'image/png', purpose: 'any' },
          {
            src: '/icons/icon-maskable-192.png',
            sizes: '192x192',
            type: 'image/png',
            purpose: 'maskable',
          },
          {
            src: '/icons/icon-maskable-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'maskable',
          },
          {
            src: '/icons/icon-monochrome-512.png',
            sizes: '512x512',
            type: 'image/png',
            purpose: 'monochrome',
          },
        ],
      },
      workbox: {
        // Precache the app shell only. API calls are never cached by the service worker:
        // offline data goes through the IndexedDB replica (src/offline).
        globPatterns: ['**/*.{js,css,html,svg,png,woff2,webmanifest,txt}'],
        // Latin fonts ship with the shell; other scripts load on demand (unicode-range).
        globIgnores: ['**/*-{cyrillic,cyrillic-ext,greek,greek-ext,vietnamese}-*.woff2'],
        navigateFallback: '/index.html',
        navigateFallbackDenylist: [/^\/api\//, /^\/licenses\.txt$/],
        cleanupOutdatedCaches: true,
      },
    }),
  ],
  // `make serve` runs the API here; production serves both from one origin.
  server: { port: 5173, strictPort: true, proxy: { '/api': 'http://127.0.0.1:8080' } },
  test: { environment: 'jsdom', setupFiles: ['./src/test-setup.ts'] },
})
