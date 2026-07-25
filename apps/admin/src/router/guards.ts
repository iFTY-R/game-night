import type { NavigationGuardWithThis, RouteLocationNormalized } from "vue-router";
import { AdminAccountState, AdminPermission, AdminSessionKind } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { routeName } from "../constants/navigation";
import { useAuthStore } from "../stores/auth";

const hasPermission = (permissions: AdminPermission[], required?: AdminPermission): boolean =>
  required == null || permissions.includes(required);

export const createAdminGuard = (): NavigationGuardWithThis<undefined> => {
  return async (to: RouteLocationNormalized) => {
    const auth = useAuthStore();
    if (!auth.restored) {
      await auth.restore();
    }

    const isAuthenticated = auth.session?.kind === AdminSessionKind.FULL;
    const sessionKind = auth.session?.kind;

    // Handle auth layout routes
    if (to.meta.layout === "auth") {
      // If fully authenticated, redirect to main app
      if (isAuthenticated && to.name !== routeName.notFound) {
        return { name: routeName.security };
      }

      // Route based on current session kind
      if (sessionKind === AdminSessionKind.SETUP_PASSWORD_PENDING && to.meta.authStep !== "changePassword") {
        return { name: routeName.authChangePassword };
      }
      if (sessionKind === AdminSessionKind.MFA_PENDING && to.meta.authStep !== "verifyMfa") {
        return { name: routeName.authVerifyMfa };
      }

      // If no session, route based on setup state
      if (!sessionKind) {
        if (auth.setupState === AdminAccountState.BOOTSTRAP_PENDING && to.meta.authStep !== "bootstrap") {
          return { name: routeName.authBootstrap };
        }
        if (auth.setupState !== AdminAccountState.BOOTSTRAP_PENDING && to.meta.authStep !== "login") {
          return { name: routeName.authLogin };
        }
      }

      return true;
    }

    // Handle protected routes - require FULL session
    if (!isAuthenticated) {
      // Route to appropriate auth step
      if (sessionKind === AdminSessionKind.SETUP_PASSWORD_PENDING) {
        return { name: routeName.authChangePassword };
      }
      if (sessionKind === AdminSessionKind.MFA_PENDING) {
        return { name: routeName.authVerifyMfa };
      }
      if (auth.setupState === AdminAccountState.BOOTSTRAP_PENDING) {
        return { name: routeName.authBootstrap };
      }
      return { name: routeName.authLogin };
    }

    // Check permission if required
    if (!hasPermission(auth.permissions, to.meta.permission)) {
      return { name: routeName.forbidden };
    }

    return true;
  };
};
