<script setup lang="ts">
import { computed } from "vue";

import { normalizeUsernameInput } from "../username";

const props = defineProps<{ username: string }>();
const emit = defineEmits<{ activate: [] }>();

const initial = computed(() => [...normalizeUsernameInput(props.username)][0] ?? "?");
const accessibleName = computed(() => `${props.username}，修改用户名`);
</script>

<template>
  <button
    class="profile-trigger"
    type="button"
    :title="accessibleName"
    :aria-label="accessibleName"
    @click="emit('activate')"
  >
    <span aria-hidden="true">{{ initial }}</span>
  </button>
</template>

<style scoped>
.profile-trigger {
  width: 42px;
  height: 42px;
  flex: 0 0 42px;
  display: grid;
  place-items: center;
  padding: 0;
  color: var(--room-accent-ink, #171b1a);
  background: var(--platform-accent);
  border: 1px solid color-mix(in srgb, var(--platform-accent) 72%, white);
  border-radius: 50%;
  box-shadow: 0 5px 16px rgb(0 0 0 / 22%);
  font-family: var(--font-display);
  font-size: 16px;
  font-weight: 900;
  line-height: 1;
}

.profile-trigger:hover { filter: brightness(1.06); }
.profile-trigger:active { transform: translateY(1px); }
</style>
