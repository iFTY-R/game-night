<script setup lang="ts">
import { ChevronRight } from "lucide-vue-next";
import { useRoute } from "vue-router";

const route = useRoute();
</script>

<template>
  <nav class="admin-breadcrumb" aria-label="当前位置">
    <div class="admin-breadcrumb__items">
      <span class="admin-muted">管理台</span>
      <ChevronRight :size="14" />
      <span>{{ route.meta.title }}</span>
    </div>
  </nav>
</template>
