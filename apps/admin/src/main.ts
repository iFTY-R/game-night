import { createApp } from "vue";
import { createPinia } from "pinia";
import App from "./App.vue";
import { createAdminRouter } from "./router";
import { useAuthStore } from "./stores/auth";
import "./styles/global.css";
import "./styles/layout.css";

const app = createApp(App);
const pinia = createPinia();

app.use(pinia);

const auth = useAuthStore(pinia);
await auth.restore();

const router = createAdminRouter(pinia);
app.use(router);

await router.isReady();

app.mount("#app");
