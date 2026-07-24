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
      "/platform.admin.v1.AdminAuthService": { ...adminEdgeProxy },
      "/platform.admin.v1.AdminIdentityService": { ...adminEdgeProxy }
    }
  },
  build: {
    target: "es2023"
  }
});
