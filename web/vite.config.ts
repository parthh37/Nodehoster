/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { writeFileSync } from 'node:fs';
import { fileURLToPath, URL } from 'node:url';

const backend = 'https://localhost:8484';
const proxied = { target: backend, secure: false, changeOrigin: true };

export default defineConfig({
  base: '/',
  plugins: [
    react(),
    {
      // The Go binary embeds the output folder (go:embed), which must hold
      // a file even before the console is built: emptyOutDir deletes the
      // committed placeholder, so put it back.
      name: 'keep-embed-placeholder',
      apply: 'build',
      closeBundle() {
        writeFileSync(new URL('../internal/webui/dist/.gitkeep', import.meta.url), '');
      },
    },
  ],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1200,
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    restoreMocks: true,
    unstubGlobals: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': proxied,
      '/hooks': proxied,
      '/metrics': proxied,
    },
  },
});
