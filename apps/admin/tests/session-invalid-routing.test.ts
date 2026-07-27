import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import {
  AdminAccountState,
  AdminPermission,
  AdminSessionKind,
  AdminSessionSummarySchema
} from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import { routeName } from "../src/constants/navigation";
import { createAdminSessionInvalidHandler } from "../src/router";
import { useAuthStore } from "../src/stores/auth";

const futureSeconds = BigInt(Math.floor(Date.now() / 1000) + 3600);

const fullSession = () => create(AdminSessionSummarySchema, {
  adminId: "admin-1",
  sessionId: "session-1",
  kind: AdminSessionKind.FULL,
  permissions: [AdminPermission.USERS_READ],
  adminVersion: 1n,
  passwordVersion: 1n,
  sessionVersion: 1n,
  idleExpiresAt: create(TimestampSchema, { seconds: futureSeconds }),
  absoluteExpiresAt: create(TimestampSchema, { seconds: futureSeconds })
});

describe("admin session invalid routing", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
  });

  it("leaves a protected route when the browser no longer sends the admin cookie", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const auth = useAuthStore(pinia);
    auth.restored = true;
    auth.setupState = AdminAccountState.ACTIVE;
    auth.applySession(fullSession());

    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: "/auth", name: routeName.authLogin, component: { template: "<main />" }, meta: { layout: "auth", title: "管理员登录" } },
        { path: "/users", name: routeName.users, component: { template: "<main />" }, meta: { layout: "admin", title: "用户中心" } }
      ]
    });
    await router.push({ name: routeName.users });
    await router.isReady();

    const handleSessionInvalid = createAdminSessionInvalidHandler(router, pinia);
    await handleSessionInvalid();

    expect(auth.session).toBeNull();
    expect(router.currentRoute.value.name).toBe(routeName.authLogin);
    expect(router.currentRoute.value.fullPath).toBe("/auth");
  });
});
