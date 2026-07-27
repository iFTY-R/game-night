<script setup lang="ts">
import { NLayout, NLayoutContent } from "naive-ui";
import { RouterView, useRoute } from "vue-router";
import AdminBreadcrumb from "./components/AdminBreadcrumb.vue";
import AdminHeader from "./components/AdminHeader.vue";
import AdminSider from "./components/AdminSider.vue";
import AdminTabs from "./components/AdminTabs.vue";
import MobileNavigation from "./components/MobileNavigation.vue";
import { useNavigationStore } from "../stores/navigation";
import { useAuthStore } from "../stores/auth";

const auth = useAuthStore();
const navigation = useNavigationStore();
const route = useRoute();

navigation.restoreTabs(auth.permissions);
// Layout creation can happen after the router has already resolved; restore persisted tabs first, then make the current route authoritative.
navigation.syncFromRoute(route, auth.permissions);
</script>

<template>
  <NLayout class="admin-layout">
    <div class="admin-layout__body">
      <AdminSider class="desktop-only" />
      <div class="admin-layout__content">
        <AdminHeader @mobile="navigation.mobileOpen = true" />
        <AdminBreadcrumb />
        <AdminTabs />
        <NLayoutContent class="admin-pane">
          <RouterView />
        </NLayoutContent>
      </div>
    </div>
    <MobileNavigation />
  </NLayout>
</template>
