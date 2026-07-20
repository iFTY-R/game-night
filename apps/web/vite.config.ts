import vue from "@vitejs/plugin-vue";
import { VitePWA } from "vite-plugin-pwa";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [
    vue(),
    VitePWA({
      registerType: "autoUpdate",
      includeAssets: ["brand-mark.svg"],
      manifest: {
        name: "Game Night",
        short_name: "Game Night",
        description: "和朋友在线围桌玩小游戏",
        theme_color: "#121a1d",
        background_color: "#121a1d",
        display: "standalone",
        orientation: "any",
        icons: [{ src: "/brand-mark.svg", sizes: "any", type: "image/svg+xml", purpose: "any maskable" }],
      },
    }),
  ],
  server: {
    host: "127.0.0.1",
    port: 4173,
  },
  build: {
    target: "es2023",
  },
});
