import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    proxy: {
      "/host": {
        target: "https://toget.vu",
        changeOrigin: true,
        secure: true,
      },
      "/reset": {
        target: "https://toget.vu",
        changeOrigin: true,
        secure: true,
      },
    },
  },
});
