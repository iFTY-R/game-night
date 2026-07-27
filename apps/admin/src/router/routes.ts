import type { RouteRecordRaw } from "vue-router";
import { AdminPermission } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
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
    path: "/auth/verify-mfa",
    name: routeName.authVerifyMfa,
    component: () => import("../views/auth/AdminAuthView.vue"),
    meta: { authStep: "verifyMfa", title: "验证多因素", layout: "auth" }
  },
  {
    path: "/",
    component: () => import("../layouts/AdminLayout.vue"),
    meta: { title: "管理台", layout: "admin" },
    children: [
      {
        path: "",
		redirect: { name: routeName.overview }
      },
		{
			path: "overview",
			name: routeName.overview,
			component: () => import("../views/overview/OverviewView.vue"),
			meta: { title: "运营概览", tab: true, closable: false, menu: true, layout: "admin", permission: AdminPermission.OVERVIEW_READ }
		},
      {
        path: "users",
        name: routeName.users,
        component: () => import("../views/users/UserCenterView.vue"),
        meta: {
          title: "用户中心",
          tab: true,
          closable: false,
          menu: true,
          layout: "admin",
          permission: AdminPermission.USERS_READ
        }
      },
      {
        path: "rooms",
        name: routeName.rooms,
        component: () => import("../views/rooms/RoomGameControlView.vue"),
        meta: {
          title: "房间与牌局",
          tab: true,
          closable: true,
          menu: true,
          layout: "admin",
          permission: AdminPermission.ROOMS_READ
        }
      },
      {
        path: "security",
        name: routeName.security,
        component: () => import("../views/security/SessionSecurityView.vue"),
        meta: {
          title: "安全设置",
          tab: true,
          closable: false,
          menu: true,
          layout: "admin",
          permission: AdminPermission.SECURITY_READ
        }
      },
      {
        path: "audit",
        name: routeName.audit,
        component: () => import("../views/audit/AuditCenterView.vue"),
        meta: {
          title: "审计中心",
          tab: true,
          closable: true,
          menu: true,
          layout: "admin",
          permission: AdminPermission.AUDIT_READ
        }
      },
		{
			path: "operations",
			name: routeName.operations,
			component: () => import("../views/operations/OperationsView.vue"),
			meta: { title: "系统运维", tab: true, closable: true, menu: true, layout: "admin", permission: AdminPermission.OPERATIONS_READ }
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
