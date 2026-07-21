import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [vue()],
  build: {
    lib: { entry: "src/index.ts", formats: ["es"], fileName: "index" },
    rollupOptions: {
      external: ["vue", "@bufbuild/protobuf", "@game-night/game-client", "@game-night/game-ui-kit", "lucide-vue-next"],
    },
  },
  test: { environment: "jsdom" },
});
