import type { NavigationGuardWithThis, RouteLocationNormalized } from "vue-router";
import { AdminNextStep, AdminPermission, AdminSessionKind, AdminSetupState } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { routeName } from "../constants/navigation";
import { useAuthStore } from "../stores/auth";

const authStepRouteMap = {
  login: routeName.authLogin,
  bootstrap: routeName.authBootstrap,
  changePassword: routeName.authChangePassword,
  enrollTotp: routeName.authEnrollTotp,
  verifyMfa: routeName.authVerifyMfa,
  rebindTotp: routeName.authRebindTotp
} as const;

const nextStepToAuthStep = (nextStep: AdminNextStep, setupState: AdminSetupState) => {
  switch (nextStep) {
    case AdminNextStep.CHANGE_PASSWORD:
      return "changePassword" as const;
    case AdminNextStep.ENROLL_TOTP:
      return "enrollTotp" as const;
    case AdminNextStep.VERIFY_MFA:
      return "verifyMfa" as const;
    case AdminNextStep.REBIND_TOTP:
      return "rebindTotp" as const;
    case AdminNextStep.AUTHENTICATED:
      return null;
    default:
      return setupState === AdminSetupState.BOOTSTRAP_PENDING ? "bootstrap" : "login";
  }
};

const hasPermission = (permissions: AdminPermission[], required?: AdminPermission): boolean =>
  required == null || permissions.includes(required);

export const createAdminGuard = (): NavigationGuardWithThis<undefined> => {
  return async (to: RouteLocationNormalized) => {
    const auth = useAuthStore();
    if (!auth.restored) {
      await auth.restore();
    }

    const restrictedStep = nextStepToAuthStep(auth.nextStep, auth.setupState);
    const isAuthenticated = auth.session?.kind === AdminSessionKind.FULL && auth.nextStep === AdminNextStep.AUTHENTICATED;

    if (to.meta.layout === "auth") {
      if (isAuthenticated && to.name !== routeName.notFound) {
        return { name: routeName.overview };
      }
      if (restrictedStep && to.meta.authStep && to.meta.authStep !== restrictedStep) {
        return { name: authStepRouteMap[restrictedStep] };
      }
      return true;
    }

    if (!isAuthenticated) {
      if (restrictedStep) {
        return { name: authStepRouteMap[restrictedStep] };
      }
      return {
        name: auth.setupState === AdminSetupState.BOOTSTRAP_PENDING ? routeName.authBootstrap : routeName.authLogin
      };
    }

    if (!hasPermission(auth.permissions, to.meta.permission)) {
      return { name: routeName.forbidden };
    }

    return true;
  };
};
