import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';
import react from '@vitejs/plugin-react';
import { resolve } from 'node:path';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': resolve(import.meta.dirname, 'src'), cn: resolve(import.meta.dirname, 'src/lib/utils.js') } },
  build: {
    lib: { entry: resolve(import.meta.dirname, 'src/main.jsx'), formats: ['es'], fileName: () => 'app.js', cssFileName: 'app' },
    outDir: resolve(import.meta.dirname, '../cmd/platform/assets'),
    emptyOutDir: false,
    minify: 'esbuild',
    rollupOptions: { output: { entryFileNames: 'app.js', assetFileNames: asset => asset.name?.endsWith('.css') ? 'app.css' : 'assets/[name]-[hash][extname]' } }
  }
});
