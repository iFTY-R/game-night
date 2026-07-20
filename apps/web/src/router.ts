import { createRouter, createWebHistory } from "vue-router";

import GameView from "./views/GameView.vue";
import HomeView from "./views/HomeView.vue";
import NotFoundView from "./views/NotFoundView.vue";
import RoomView from "./views/RoomView.vue";

export const router = createRouter({
  history: createWebHistory(),
  scrollBehavior: () => ({ top: 0 }),
  routes: [
    { path: "/", name: "home", component: HomeView, meta: { title: "开始一局" } },
    { path: "/room/:roomId", name: "room", component: RoomView, props: true, meta: { title: "房间" } },
    { path: "/room/:roomId/game/:sessionId", name: "game", component: GameView, props: true, meta: { title: "游戏桌" } },
    { path: "/fixtures/table", name: "fixture-table", component: GameView, props: { roomId: "fixture-room", sessionId: "fixture-session" }, meta: { title: "桌面预览" } },
    { path: "/:pathMatch(.*)*", name: "not-found", component: NotFoundView, meta: { title: "找不到页面" } },
  ],
});

router.afterEach((to) => {
  document.title = `${String(to.meta.title ?? "Game Night")} · Game Night`;
});
