import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { defineConfig, type Plugin } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

function keepEmbedPlaceholder(): Plugin {
  return {
    name: 'issuetap-embed-placeholder',
    closeBundle() {
      writeFileSync(
        'dist/app/.placeholder',
        'Placeholder so the go:embed directive in embed.go always has a directory.\n' +
          'Run `npm run build` to produce the real web assets here.\n',
      )
    },
  }
}

export default defineConfig({
  root: 'web',
  base: '/',
  plugins: [svelte(), keepEmbedPlaceholder()],
  build: {
    outDir: resolve('dist/app'),
    emptyOutDir: true,
  },
  server: {
    host: '127.0.0.1',
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
      '/rest': 'http://127.0.0.1:8080',
      '/wiki': 'http://127.0.0.1:8080',
    },
  },
})
