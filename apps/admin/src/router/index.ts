import { createRouter, createWebHistory } from "vue-router";
import type { Pinia } from "pinia";
import { routes } from "./routes";
import { createAdminGuard } from "./guards";
import { useNavigationStore } from "../stores/navigation";

export const createAdminRouter = (pinia: Pinia) => {
  const router = createRouter({
    history: createWebHistory(),
    routes
  });
  router.beforeEach(createAdminGuard());
  router.afterEach((to) => {
    useNavigationStore(pinia).syncFromRoute(to);
  });
  return router;
};
