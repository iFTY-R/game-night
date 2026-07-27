import { createRouter, createWebHistory, type Router } from "vue-router";
import type { Pinia } from "pinia";
import { routes } from "./routes";
import { createAdminGuard } from "./guards";
import { routeName } from "../constants/navigation";
import { installSessionInvalidHandler } from "../api/connect";
import { AdminAccountState } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { useNavigationStore } from "../stores/navigation";
import { useAuthStore } from "../stores/auth";

/**
 * Binds transport-level authentication loss to the active router instance.
 * The transport is intentionally router-agnostic; this adapter owns the visible transition out of
 * protected screens and coalesces simultaneous failed requests into one navigation.
 */
export const createAdminSessionInvalidHandler = (router: Router, pinia: Pinia): (() => Promise<void>) => {
  let redirecting = false;

  return async (): Promise<void> => {
    const auth = useAuthStore(pinia);
    auth.invalidateSession();

    if (redirecting || router.currentRoute.value.meta.layout === "auth") {
      return;
    }

    redirecting = true;
    const destination = auth.setupState === AdminAccountState.BOOTSTRAP_PENDING
      ? routeName.authBootstrap
      : routeName.authLogin;

    await router.replace({ name: destination }).finally(() => {
      redirecting = false;
    });
  };
};

/**
 * Installs the current router's session-loss handler into the singleton transport adapter.
 * The adapter intentionally discards the promise because Connect error parsing must not delay the
 * original rejected request; the handler itself serializes navigation before a later failure can act.
 */
export const installAdminSessionInvalidHandler = (router: Router, pinia: Pinia): void => {
  const handleSessionInvalid = createAdminSessionInvalidHandler(router, pinia);
  installSessionInvalidHandler(() => {
    void handleSessionInvalid();
  });
};

export const createAdminRouter = (pinia: Pinia) => {
  const router = createRouter({
    history: createWebHistory(),
    routes
  });
  router.beforeEach(createAdminGuard());
  router.afterEach((to) => {
    // Tab state is permission-aware; omitting the server-issued permission set silently leaves an older tab active.
    useNavigationStore(pinia).syncFromRoute(to, useAuthStore(pinia).permissions);
  });
  installAdminSessionInvalidHandler(router, pinia);
  return router;
};
