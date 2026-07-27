import vue from "@vitejs/plugin-vue";
import { defineConfig } from "vite";

// Edge selects the isolated admin surface from Host while the browser keeps the Vite origin.
const adminEdgeProxy = {
  target: "http://127.0.0.1:8080",
  headers: { host: "admin.localhost:8080" }
};

export default defineConfig({
  plugins: [vue()],
  server: {
    host: "127.0.0.1",
    port: 4174,
    proxy: {
      // Keep every mounted administrator RPC on the same browser origin during local development.
      "/platform.admin.v1.AdminAuthService": { ...adminEdgeProxy },
      "/platform.admin.v1.AdminUserService": { ...adminEdgeProxy },
      "/platform.admin.v1.AdminRoomService": { ...adminEdgeProxy },
      "/platform.admin.v1.AdminAuditService": { ...adminEdgeProxy },
      "/platform.admin.v1.AdminOperationsService": { ...adminEdgeProxy },
      "/platform.admin.v1.AdminOverviewService": { ...adminEdgeProxy }
    }
  },
  build: {
    target: "es2023"
  }
});
