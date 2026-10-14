import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import { writeFileSync } from 'node:fs';
import { resolve } from 'node:path';

// The Go server embeds the build from backend/internal/web/dist. Vite empties
// that folder on each build, so put back the placeholder Git tracks.
const outDir = resolve(__dirname, '../backend/internal/web/dist');
const keepPlaceholder = (): Plugin => ({
  name: 'keep-gitkeep',
  closeBundle() {
    writeFileSync(resolve(outDir, '.gitkeep'), '');
  },
});

export default defineConfig({
  plugins: [react(), keepPlaceholder()],
  build: { outDir, emptyOutDir: true, sourcemap: false },
  server: {
    port: 5173,
    // In development the Go server runs on :8080 and Vite proxies the API.
    proxy: { '/api': { target: 'http://localhost:8080', changeOrigin: false } },
  },
});
