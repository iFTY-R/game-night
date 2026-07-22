import { createApp } from "vue";
import { createPinia } from "pinia";

import App from "./App.vue";
import { router } from "./router";
import { useRoomStore } from "./stores/room";
import "./styles/global.css";

const app = createApp(App);
const pinia = createPinia();

app.use(pinia);
// Recover viewer-safe navigation context before any route component initializes from it.
const roomStore = useRoomStore(pinia);
roomStore.recover();
// Device authentication is best-effort during shell startup; protected actions
// retry it explicitly after the user submits onboarding or enters a room.
void roomStore.recoverIdentity();
app.use(router).mount("#app");
