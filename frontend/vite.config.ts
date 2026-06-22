import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react()],
  build: {
    // Masaüstü uygulaması: asset'ler embedded (ağ indirme yok), bu yüzden tek
    // büyük chunk pratikte sorun değil. Vite 8'in 500 kB uyarısını sustur.
    chunkSizeWarningLimit: 1000,
  },
  server: {
    port: 5199,
    headers: {
      'Cache-Control': 'no-store',
    },
  },
})
