import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The Go backend owns the radios and serial ports, so the dev server proxies
// both the REST API and the websocket to it rather than the browser talking to
// hardware itself.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/ws': { target: 'ws://127.0.0.1:8080', ws: true },
    },
  },
})
