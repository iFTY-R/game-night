import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import ReadinessStatus from "../src/components/session/ReadinessStatus.vue";

describe("ReadinessStatus", () => {
  it("maps the current backend readiness payload to operator-facing labels", () => {
    const wrapper = mount(ReadinessStatus, {
      props: {
        title: "敏感写入状态",
        readiness: {
          mode: "sensitive_write",
          ready: false,
          components: {
            postgresql: "ready",
            redis: "unavailable",
            keyring: "ready",
            bootstrap: "ready",
            checkpoint: "unavailable"
          }
        }
      }
    });

    expect(wrapper.text()).toContain("敏感写入状态");
    expect(wrapper.text()).toContain("检查类型：敏感写入");
    expect(wrapper.text()).toContain("数据库");
    expect(wrapper.text()).toContain("缓存");
    expect(wrapper.text()).toContain("密钥环");
    expect(wrapper.text()).toContain("启动协调");
    expect(wrapper.text()).toContain("审计检查点");
    expect(wrapper.text()).toContain("可用");
    expect(wrapper.text()).toContain("不可用");
    expect(wrapper.text()).not.toContain("object_storage");
    expect(wrapper.text()).not.toContain("not_ready");
  });

  it("keeps unknown values visible as a fallback", () => {
    const wrapper = mount(ReadinessStatus, {
      props: {
        title: "普通服务状态",
        readiness: {
          mode: "custom_mode",
          ready: false,
          components: {
            mystery_dependency: "degraded"
          }
        }
      }
    });

    expect(wrapper.text()).toContain("custom_mode");
    expect(wrapper.text()).toContain("mystery_dependency");
    expect(wrapper.text()).toContain("degraded");
  });

  it("renders the empty state before readiness is loaded", () => {
    const wrapper = mount(ReadinessStatus, {
      props: {
        title: "普通服务状态",
        readiness: null
      }
    });

    expect(wrapper.text()).toContain("尚未读取服务状态。");
  });
});
