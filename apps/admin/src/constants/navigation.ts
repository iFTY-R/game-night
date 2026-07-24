import { BookCheck, LayoutDashboard, ShieldAlert, Users } from "lucide-vue-next";
import { AdminPermission } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";

export const routeName = {
  authLogin: "auth-login",
  authBootstrap: "auth-bootstrap",
  authChangePassword: "auth-change-password",
  authEnrollTotp: "auth-enroll-totp",
  authVerifyMfa: "auth-verify-mfa",
  authRebindTotp: "auth-rebind-totp",
  overview: "overview",
  users: "users",
  audit: "audit",
  security: "security",
  forbidden: "forbidden",
  notFound: "not-found"
} as const;

export type AppRouteName = (typeof routeName)[keyof typeof routeName];

export type NavigationItem = {
  name: AppRouteName;
  title: string;
  icon: typeof LayoutDashboard;
  permission?: AdminPermission;
};

export const navigationItems: NavigationItem[] = [
  { name: routeName.overview, title: "概览", icon: LayoutDashboard },
  { name: routeName.users, title: "用户治理", icon: Users, permission: AdminPermission.GET_USER },
  { name: routeName.audit, title: "审计", icon: BookCheck, permission: AdminPermission.READ_AUDIT },
  { name: routeName.security, title: "会话安全", icon: ShieldAlert }
];
