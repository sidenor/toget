import { readFileSync } from "node:fs";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const hostTarget = process.env.TOGET_HOST_PROXY || "https://toget.vu";
const devCert = process.env.TOGET_DEV_CERT;
const devKey = process.env.TOGET_DEV_KEY;

export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    https:
      devCert && devKey
        ? {
            cert: readFileSync(devCert),
            key: readFileSync(devKey),
          }
        : undefined,
    proxy: {
      "/host": {
        target: hostTarget,
        changeOrigin: true,
        secure: process.env.TOGET_HOST_INSECURE !== "1",
      },
      "/reset": {
        target: hostTarget,
        changeOrigin: true,
        secure: process.env.TOGET_HOST_INSECURE !== "1",
      },
    },
  },
});
