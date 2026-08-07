import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vite";

// The admin SPA is mounted below /admin in both development and the bundled edge static server.
const adminEdgeProxy = {
  target: "http://127.0.0.1:8080"
};

export default defineConfig({
  plugins: [vue()],
  base: "/admin/",
  server: {
    host: "127.0.0.1",
    port: 4174,
    proxy: {
      // Keep every mounted administrator RPC on the same browser origin during local development.
      "/admin/platform.admin.v1.AdminAuthService": { ...adminEdgeProxy },
      "/admin/platform.admin.v1.AdminUserService": { ...adminEdgeProxy },
      "/admin/platform.admin.v1.AdminRoomService": { ...adminEdgeProxy },
      "/admin/platform.admin.v1.AdminAuditService": { ...adminEdgeProxy },
      "/admin/platform.admin.v1.AdminOperationsService": { ...adminEdgeProxy },
      "/admin/platform.admin.v1.AdminOverviewService": { ...adminEdgeProxy }
    }
  },
  build: {
    target: "es2023"
  }
});
