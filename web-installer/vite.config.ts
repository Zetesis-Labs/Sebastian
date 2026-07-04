import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Static app for GitHub Pages, published under docs/installer. Relative base so
// it works from any Pages subpath. Assets in public/ (manifest, schema, brand,
// firmware/) are copied verbatim next to the build.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  base: "./",
  build: {
    outDir: "../docs/installer",
    // Do NOT wipe the whole dir — it holds user-owned files (sebastian-config
    // .local.json, firmware/). The build script cleans only the hashed assets.
    emptyOutDir: false,
  },
});
