<script setup lang="ts">
import type { AdminPermission } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { useAuthStore } from "../stores/auth";

const props = defineProps<{
  permission?: AdminPermission;
}>();

const auth = useAuthStore();
</script>

<template>
  <slot v-if="!props.permission || auth.permissions.includes(props.permission)" />
  <slot v-else name="fallback" />
</template>
