import { writeFileSync } from "node:fs";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [
    react(),
    {
      // Vite empties dist on each build. The tracked .gitkeep lets `go vet`
      // and `go build` embed frontend/dist in a fresh clone.
      name: "keep-dist",
      closeBundle() {
        writeFileSync(new URL("./dist/.gitkeep", import.meta.url), "");
      },
    },
  ],
  build: {
    target: "safari15",
    cssTarget: "safari15",
    chunkSizeWarningLimit: 2000,
  },
});
