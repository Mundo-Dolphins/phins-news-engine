import { defineConfig } from "vitest/config";
import preact from "@preact/preset-vite";

export default defineConfig({
  base: "/",
  plugins: [preact()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8480",
      "/healthz": "http://127.0.0.1:8480",
    },
  },
  test: { environment: "jsdom", globals: true, setupFiles: ["src/test-setup.ts"] },
});
