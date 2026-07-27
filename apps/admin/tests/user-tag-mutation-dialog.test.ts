import { create } from "@bufbuild/protobuf";
import { flushPromises, mount } from "@vue/test-utils";
import { NMessageProvider } from "naive-ui";
import { defineComponent, h, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AdminUserTagSchema } from "../../../contracts/gen/ts/platform/admin/v1/admin_user_pb";

const api = vi.hoisted(() => ({
  deleteUserTag: vi.fn(),
  updateUserTag: vi.fn()
}));

vi.mock("../src/api/admin-user", async () => {
  const actual = await vi.importActual<typeof import("../src/api/admin-user")>("../src/api/admin-user");
  return {
    ...actual,
    deleteUserTag: api.deleteUserTag,
    updateUserTag: api.updateUserTag
  };
});

vi.mock("../src/api/connect", async () => {
  const actual = await vi.importActual<typeof import("../src/api/connect")>("../src/api/connect");
  return {
    ...actual,
    createOperationId: () => "operation-fixed"
  };
});

import UserTagMutationDialog from "../src/views/users/components/UserTagMutationDialog.vue";

const AppDialogStub = defineComponent({
  name: "AppDialogStub",
  emits: ["closed"],
  setup(_, { emit, expose, slots }) {
    const visible = ref(false);
    const close = (): void => {
      if (!visible.value) {
        return;
      }
      visible.value = false;
      emit("closed");
    };
    expose({
      toggleDialog(open: boolean) {
        if (open) {
          visible.value = true;
          return;
        }
        close();
      }
    });
    return () => visible.value ? h("section", { class: "app-dialog-stub" }, slots.default?.({ close })) : null;
  }
});

const tag = (overrides: Partial<Record<string, unknown>> = {}) => create(AdminUserTagSchema, {
  tagId: "tag-1",
  name: "高风险",
  color: "#DC2626",
  version: 4n,
  ...overrides
});

const Host = defineComponent({
  setup() {
    return () => h(NMessageProvider, null, {
      default: () => h(UserTagMutationDialog)
    });
  }
});

const mountDialog = () => {
  const host = mount(Host, {
    global: {
      stubs: { AppDialog: AppDialogStub }
    }
  });
  return host.findComponent(UserTagMutationDialog);
};

const openDialog = async (
  wrapper: ReturnType<typeof mountDialog>,
  mode: "edit" | "delete",
  selectedTag = tag()
): Promise<void> => {
  const dialog = wrapper.vm as unknown as {
    toggleDialog: (open: boolean, payload: { mode: "edit" | "delete"; tag: ReturnType<typeof tag> }) => void;
  };
  dialog.toggleDialog(true, { mode, tag: selectedTag });
  await flushPromises();
};

describe("user tag mutation dialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("updates a version-bound tag and canonicalizes the operator-entered color case", async () => {
    const updated = tag({ name: "需复核", color: "#2563EB", version: 5n });
    api.updateUserTag.mockResolvedValue({ tag: updated });
    const wrapper = mountDialog();

    await openDialog(wrapper, "edit");
    const inputs = wrapper.findAll("input");
    await inputs[0]!.setValue("需复核");
    await inputs[1]!.setValue("#2563eb");
    await wrapper.find("textarea").setValue("调整风险标签的展示名称");
    await wrapper.findAll("button").find((button) => button.text().includes("保存标签"))!.trigger("click");
    await flushPromises();

    // Lowercase operator input must reach the API in the canonical uppercase form the
    // backend and PostgreSQL CHECK constraint enforce.
    expect(api.updateUserTag).toHaveBeenCalledWith({
      operationId: "operation-fixed",
      tagId: "tag-1",
      name: "需复核",
      color: "#2563EB",
      reason: "调整风险标签的展示名称",
      expectedVersion: 4n,
      signal: expect.any(AbortSignal)
    });
    expect(wrapper.emitted("updated")).toEqual([[updated]]);
  });

  it("deletes a tag with an explicit reason and returns its catalog ID", async () => {
    api.deleteUserTag.mockResolvedValue({ tagId: "tag-1" });
    const wrapper = mountDialog();

    await openDialog(wrapper, "delete");
    await wrapper.find("textarea").setValue("合并重复标签");
    await wrapper.findAll("button").find((button) => button.text().includes("确认删除"))!.trigger("click");
    await flushPromises();

    expect(api.deleteUserTag).toHaveBeenCalledWith({
      operationId: "operation-fixed",
      tagId: "tag-1",
      reason: "合并重复标签",
      expectedVersion: 4n,
      signal: expect.any(AbortSignal)
    });
    expect(wrapper.emitted("deleted")).toEqual([["tag-1"]]);
  });
});
