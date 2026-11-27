import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development the Go server runs on :8080 (make dev) and Vite proxies the API to it.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": { target: "http://127.0.0.1:8080", ws: true } },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 4000,
  },
  define: { "process.env.IS_PREACT": JSON.stringify("false") },
});
