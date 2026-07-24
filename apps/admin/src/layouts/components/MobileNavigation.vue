<script setup lang="ts">
import { NDrawer, NDrawerContent } from "naive-ui";
import { useNavigationStore } from "../../stores/navigation";
import AdminSider from "./AdminSider.vue";

const navigation = useNavigationStore();
</script>

<template>
  <NDrawer v-model:show="navigation.mobileOpen" placement="left" :width="280">
    <NDrawerContent title="管理导航" closable>
      <AdminSider mobile />
    </NDrawerContent>
  </NDrawer>
</template>
