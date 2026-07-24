import type { RouteRecordRaw } from "vue-router";
import { AdminPermission } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { routeName } from "../constants/navigation";

export const routes: RouteRecordRaw[] = [
  {
    path: "/auth",
    name: routeName.authLogin,
    component: () => import("../views/auth/AdminAuthView.vue"),
    meta: { authStep: "login", title: "管理员登录", layout: "auth" }
  },
  {
    path: "/auth/bootstrap",
    name: routeName.authBootstrap,
    component: () => import("../views/auth/AdminAuthView.vue"),
    meta: { authStep: "bootstrap", title: "等待初始化", layout: "auth" }
  },
  {
    path: "/auth/change-password",
    name: routeName.authChangePassword,
    component: () => import("../views/auth/AdminAuthView.vue"),
    meta: { authStep: "changePassword", title: "修改初始密码", layout: "auth" }
  },
  {
    path: "/auth/enroll-totp",
    name: routeName.authEnrollTotp,
    component: () => import("../views/auth/AdminAuthView.vue"),
    meta: { authStep: "enrollTotp", title: "绑定身份验证器", layout: "auth" }
  },
  {
    path: "/auth/verify-mfa",
    name: routeName.authVerifyMfa,
    component: () => import("../views/auth/AdminAuthView.vue"),
    meta: { authStep: "verifyMfa", title: "验证多因素", layout: "auth" }
  },
  {
    path: "/auth/rebind-totp",
    name: routeName.authRebindTotp,
    component: () => import("../views/auth/AdminAuthView.vue"),
    meta: { authStep: "rebindTotp", title: "重绑验证器", layout: "auth" }
  },
  {
    path: "/",
    component: () => import("../layouts/AdminLayout.vue"),
    meta: { title: "管理台", layout: "admin" },
    children: [
      {
        path: "",
        name: routeName.overview,
        component: () => import("../views/dashboard/OverviewView.vue"),
        meta: { title: "概览", tab: true, closable: false, menu: true, layout: "admin" }
      },
      {
        path: "users",
        name: routeName.users,
        component: () => import("../views/users/UserWorkbenchView.vue"),
        meta: {
          title: "用户治理",
          tab: true,
          closable: true,
          menu: true,
          layout: "admin",
          permission: AdminPermission.GET_USER
        }
      },
      {
        path: "audit",
        name: routeName.audit,
        component: () => import("../views/audit/AuditView.vue"),
        meta: {
          title: "审计",
          tab: true,
          closable: true,
          menu: true,
          layout: "admin",
          permission: AdminPermission.READ_AUDIT
        }
      },
      {
        path: "security",
        name: routeName.security,
        component: () => import("../views/security/SessionSecurityView.vue"),
        meta: { title: "会话安全", tab: true, closable: true, menu: true, layout: "admin" }
      },
      {
        path: "403",
        name: routeName.forbidden,
        component: () => import("../views/errors/ForbiddenView.vue"),
        meta: { title: "无权访问", tab: false, menu: false, layout: "admin" }
      }
    ]
  },
  {
    path: "/:pathMatch(.*)*",
    name: routeName.notFound,
    component: () => import("../views/errors/NotFoundView.vue"),
    meta: { title: "页面不存在", layout: "auth" }
  }
];
