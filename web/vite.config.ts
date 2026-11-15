import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// В dev-режиме (`npm run dev`) запросы /api проксируются на Go-сервер,
// который должен быть запущен на :8080.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
