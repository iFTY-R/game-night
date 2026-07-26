import { ShieldAlert, UsersRound } from "lucide-vue-next";
import { AdminPermission } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

export const routeName = {
  authLogin: "auth-login",
  authBootstrap: "auth-bootstrap",
  authChangePassword: "auth-change-password",
  authVerifyMfa: "auth-verify-mfa",
  users: "users",
  security: "security",
  forbidden: "forbidden",
  notFound: "not-found"
} as const;

export type AppRouteName = (typeof routeName)[keyof typeof routeName];

export type NavigationItem = {
  name: AppRouteName;
  title: string;
  icon: typeof ShieldAlert | typeof UsersRound;
  permission: AdminPermission;
};

// Only modules with real backend and frontend flows are exposed in the stage menu.
export const navigationItems: NavigationItem[] = [
  { name: routeName.users, title: "用户中心", icon: UsersRound, permission: AdminPermission.USERS_READ },
  { name: routeName.security, title: "安全设置", icon: ShieldAlert, permission: AdminPermission.SECURITY_READ }
];
