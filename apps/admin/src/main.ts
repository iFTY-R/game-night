import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import { createAdminRouter } from "./router";
import { useAuthStore } from "./stores/auth";
import { usePreferencesStore } from "./stores/preferences";
import "./styles/global.css";
import "./styles/layout.css";

const app = createApp(App);
const pinia = createPinia();

app.use(pinia);

// Apply persisted/system theme tokens before authentication restoration can delay the first Vue render.
const preferences = usePreferencesStore(pinia);
document.documentElement.dataset.theme = preferences.resolvedTheme;

const auth = useAuthStore(pinia);
await auth.restore();

const router = createAdminRouter(pinia);
app.use(router);

await router.isReady();

app.mount("#app");
