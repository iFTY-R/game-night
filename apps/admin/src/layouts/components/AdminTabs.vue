<script setup lang="ts">
import { X } from "lucide-vue-next";
import { NButton } from "naive-ui";
import { useRouter } from "vue-router";
import { useNavigationStore } from "../../stores/navigation";

const navigation = useNavigationStore();
const router = useRouter();

const handleClose = async (name: string): Promise<void> => {
  const next = navigation.closeTab(name as never);
  await router.push({ name: next });
};
</script>

<template>
  <section class="admin-tabs" role="tablist" aria-label="已打开页面">
    <div
      v-for="tab in navigation.tabs"
      :key="tab.name"
      class="admin-tab"
      :class="{ 'is-active': navigation.activeTab === tab.name }"
    >
      <router-link
        :to="{ name: tab.name }"
        role="tab"
        :aria-selected="navigation.activeTab === tab.name"
      >
        {{ tab.title || tab.name }}
      </router-link>
      <NButton
        v-if="tab.closable"
        text
        aria-label="关闭标签页"
        @click.prevent="handleClose(tab.name)"
      >
        <template #icon>
          <X :size="13" />
        </template>
      </NButton>
    </div>
  </section>
</template>
