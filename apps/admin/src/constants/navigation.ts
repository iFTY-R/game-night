import type { Component } from "vue";
import { Activity, FileClock, Gamepad2, ShieldAlert, UsersRound, Wrench } from "lucide-vue-next";
import { AdminPermission } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

export const routeName = {
  authLogin: "auth-login",
  authBootstrap: "auth-bootstrap",
  authChangePassword: "auth-change-password",
  authVerifyMfa: "auth-verify-mfa",
  overview: "overview",
  users: "users",
  rooms: "rooms",
  security: "security",
  audit: "audit",
  operations: "operations",
  forbidden: "forbidden",
  notFound: "not-found"
} as const;

export type AppRouteName = (typeof routeName)[keyof typeof routeName];

export type NavigationItem = {
  name: AppRouteName;
  title: string;
  icon: Component;
  permission: AdminPermission;
};

// The shared list is the only desktop/mobile navigation source and follows the approved six-module order.
export const navigationItems: NavigationItem[] = [
	{ name: routeName.overview, title: "运营概览", icon: Activity, permission: AdminPermission.OVERVIEW_READ },
  { name: routeName.users, title: "用户中心", icon: UsersRound, permission: AdminPermission.USERS_READ },
  { name: routeName.rooms, title: "房间与牌局", icon: Gamepad2, permission: AdminPermission.ROOMS_READ },
	{ name: routeName.audit, title: "审计中心", icon: FileClock, permission: AdminPermission.AUDIT_READ },
	{ name: routeName.security, title: "安全设置", icon: ShieldAlert, permission: AdminPermission.SECURITY_READ },
	{ name: routeName.operations, title: "系统运维", icon: Wrench, permission: AdminPermission.OPERATIONS_READ }
];
