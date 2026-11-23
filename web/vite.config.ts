import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  // `make serve` runs the API here; production serves both from one origin.
  server: { port: 5173, strictPort: true, proxy: { '/api': 'http://127.0.0.1:8080' } },
  test: { environment: 'jsdom', setupFiles: ['./src/test-setup.ts'] },
})
