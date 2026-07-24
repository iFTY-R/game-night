/// <reference types="vite/client" />

import type { AdminPermission } from "../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";

declare module "vue-router" {
  interface RouteMeta {
    authStep?: "login" | "bootstrap" | "changePassword" | "enrollTotp" | "verifyMfa" | "rebindTotp";
    permission?: AdminPermission;
    title: string;
    tab?: boolean;
    closable?: boolean;
    menu?: boolean;
    layout?: "auth" | "admin";
  }
}
