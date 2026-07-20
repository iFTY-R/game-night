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
useRoomStore(pinia).recover();
app.use(router).mount("#app");
