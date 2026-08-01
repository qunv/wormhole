import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  base: "/admin/",
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../../internal/adminui/dist",
    emptyOutDir: true,
    sourcemap: false,
    target: "es2022",
  },
  server: {
    host: "127.0.0.1",
    port: 5178,
    strictPort: true,
    proxy: {
      "/admin/api": {
        target: "http://127.0.0.1:8132",
        changeOrigin: true,
        headers: {
          Origin: "http://127.0.0.1:8132",
        },
      },
    },
  },
});
