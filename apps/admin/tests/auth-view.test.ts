import { createPinia, setActivePinia } from "pinia";
import { mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it } from "vitest";
import { createMemoryHistory, createRouter } from "vue-router";
import { AdminAccountState } from "../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import AdminAuthView from "../src/views/auth/AdminAuthView.vue";
import { routes } from "../src/router/routes";
import { useAuthStore } from "../src/stores/auth";

describe("AdminAuthView", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  it("renders the deployment initialization state", async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes
    });
    await router.push("/auth/bootstrap");
    await router.isReady();

    const store = useAuthStore();
    store.setupState = AdminAccountState.BOOTSTRAP_PENDING;
    store.startLogin = async () => undefined;

    const wrapper = mount(AdminAuthView, {
      global: {
        plugins: [router]
      }
    });

    expect(wrapper.text()).toContain("等待部署初始化");
    expect(wrapper.text()).not.toContain("backend");
  });
});
