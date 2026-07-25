import { ShieldAlert } from "lucide-vue-next";

export const routeName = {
  authLogin: "auth-login",
  authBootstrap: "auth-bootstrap",
  authChangePassword: "auth-change-password",
  authVerifyMfa: "auth-verify-mfa",
  security: "security",
  forbidden: "forbidden",
  notFound: "not-found"
} as const;

export type AppRouteName = (typeof routeName)[keyof typeof routeName];

export type NavigationItem = {
  name: AppRouteName;
  title: string;
  icon: typeof ShieldAlert;
};

// Security is the only module whose backend and frontend flows are complete in this phase.
export const navigationItems: NavigationItem[] = [
  { name: routeName.security, title: "安全设置", icon: ShieldAlert }
];
