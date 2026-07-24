import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import { AdminPermission, AdminSessionKind, AdminSessionSummarySchema } from "../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { routeName } from "../src/constants/navigation";
import { useAuthStore } from "../src/stores/auth";
import { useNavigationStore } from "../src/stores/navigation";
import { usePreferencesStore } from "../src/stores/preferences";

const buildSession = (permissions: AdminPermission[]) =>
  create(AdminSessionSummarySchema, {
    adminId: "admin-1",
    kind: AdminSessionKind.FULL,
    permissions,
    idleExpiresAt: create(TimestampSchema, { seconds: 1n }),
    absoluteExpiresAt: create(TimestampSchema, { seconds: 2n })
  });

describe("navigation store", () => {
  beforeEach(() => {
    localStorage.clear();
    setActivePinia(createPinia());
  });

  it("restores persisted tabs from the current session permissions with administrator-facing titles", () => {
    const auth = useAuthStore();
    const preferences = usePreferencesStore();
    auth.session = buildSession([AdminPermission.GET_USER]);
    preferences.persistedTabs = [routeName.overview, routeName.users];

    const navigation = useNavigationStore();
    navigation.restoreTabs();

    expect(navigation.tabs.map(({ name, title }) => ({ name, title }))).toEqual([
      { name: routeName.overview, title: "概览" },
      { name: routeName.users, title: "用户治理" }
    ]);
    expect(preferences.persistedTabs).toEqual([routeName.overview, routeName.users]);
  });

  it("drops disallowed persisted tabs and writes the normalized list back", () => {
    const auth = useAuthStore();
    const preferences = usePreferencesStore();
    auth.session = buildSession([]);
    preferences.persistedTabs = [routeName.users, routeName.overview, routeName.audit, routeName.security, routeName.users];

    const navigation = useNavigationStore();
    navigation.restoreTabs();

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.overview, routeName.security]);
    expect(preferences.persistedTabs).toEqual([routeName.overview, routeName.security]);
  });

  it("keeps overview even when the stored tabs are entirely unauthorized", () => {
    const auth = useAuthStore();
    const preferences = usePreferencesStore();
    auth.session = buildSession([]);
    preferences.persistedTabs = [routeName.users, routeName.audit];

    const navigation = useNavigationStore();
    navigation.restoreTabs();

    expect(navigation.tabs.map(({ name }) => name)).toEqual([routeName.overview]);
    expect(preferences.persistedTabs).toEqual([routeName.overview]);
  });
});
