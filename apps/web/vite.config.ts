import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The dev server forwards /api (REST and WebSockets) to the gateway, so the browser sees one origin and no CORS is needed.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: 'http://localhost:3000', ws: true },
    },
  },
});
