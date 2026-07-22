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
    // Keep browser traffic same-origin in development, matching the public reverse-proxy contract.
    proxy: {
      "/platform.identity.v1.IdentityService": "http://127.0.0.1:8080",
      "/platform.room.v1.RoomService": "http://127.0.0.1:8080",
      "/platform.game.v1.GameService": "http://127.0.0.1:8080",
      "/realtime/game": {
        target: "ws://127.0.0.1:8090",
        ws: true,
      },
    },
  },
  build: {
    target: "es2023",
  },
});
