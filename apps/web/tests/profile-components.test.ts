import { createPinia, setActivePinia } from "pinia";
import { createApp, nextTick, type ComponentPublicInstance } from "vue";
import { afterEach, describe, expect, it, vi } from "vitest";

import ProfileTrigger from "../src/components/ProfileTrigger.vue";
import UsernameDialog, { type UsernameDialogMode } from "../src/components/UsernameDialog.vue";
import { useRoomStore } from "../src/stores/room";

type UsernameDialogInstance = ComponentPublicInstance & { open: (mode?: UsernameDialogMode) => void };

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.replaceChildren();
});

describe("profile controls", () => {
  it("renders a stable first-character profile trigger with the full accessible name", async () => {
    const activated = vi.fn();
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp(ProfileTrigger, { username: "小满", onActivate: activated });
    app.mount(root);

    const button = root.querySelector<HTMLButtonElement>("button");
    expect(button?.textContent?.trim()).toBe("小");
    expect(button?.getAttribute("aria-label")).toBe("小满，修改用户名");
    button?.click();
    expect(activated).toHaveBeenCalledOnce();

    app.unmount();
  });

  it("opens in conflict mode, validates, changes the username, and restores focus", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const room = useRoomStore(pinia);
    room.$patch({ userId: "user-1", displayName: "小满", identityState: "active" });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      user: { userId: "user-1", status: "USER_STATUS_ACTIVE", username: "阿青" },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    const changed = vi.fn();
    const opener = document.createElement("button");
    document.body.appendChild(opener);
    opener.focus();
    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp(UsernameDialog, { onChanged: changed });
    app.use(pinia);
    const dialog = app.mount(root) as UsernameDialogInstance;

    dialog.open("room-conflict");
    await nextTick();
    const input = document.body.querySelector<HTMLInputElement>("#username-dialog-input");
    const submit = document.body.querySelector<HTMLButtonElement>(".username-dialog__submit");
    expect(document.body.textContent).toContain("房间内已有同名玩家");
    expect(submit?.textContent).toContain("改名并进房");
    expect(input?.value).toBe("小满");
    expect(submit?.disabled).toBe(true);

    if (input) {
      input.value = "阿青";
      input.dispatchEvent(new Event("input", { bubbles: true }));
    }
    await nextTick();
    expect(submit?.disabled).toBe(false);
    document.body.querySelector<HTMLFormElement>(".username-dialog__form")
      ?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(changed).toHaveBeenCalledWith({ username: "阿青", mode: "room-conflict" }));
    await nextTick();

    expect(document.body.querySelector("dialog")?.hasAttribute("open")).toBe(false);
    expect(document.activeElement).toBe(opener);
    app.unmount();
  });

  it("keeps server errors inside the dialog without changing the current username", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const room = useRoomStore(pinia);
    room.$patch({ userId: "user-1", displayName: "小满", identityState: "active" });
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      code: "already_exists",
      message: "room.username.taken",
    }), { status: 409, headers: { "Content-Type": "application/json" } })));

    const root = document.createElement("div");
    document.body.appendChild(root);
    const app = createApp(UsernameDialog);
    app.use(pinia);
    const dialog = app.mount(root) as UsernameDialogInstance;
    dialog.open();
    await nextTick();
    const input = document.body.querySelector<HTMLInputElement>("#username-dialog-input");
    if (input) {
      input.value = "阿青";
      input.dispatchEvent(new Event("input", { bubbles: true }));
    }
    await nextTick();
    document.body.querySelector<HTMLFormElement>(".username-dialog__form")
      ?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));

    await vi.waitFor(() => expect(document.body.textContent).toContain("房间内已有同名玩家"));
    expect(room.displayName).toBe("小满");
    expect(document.body.querySelector("dialog")?.hasAttribute("open")).toBe(true);
    app.unmount();
  });
});
