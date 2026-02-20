import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5199,
    headers: {
      'Cache-Control': 'no-store',
    },
  },
})
