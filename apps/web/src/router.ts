import { createRouter, createWebHistory } from "vue-router";

import GameView from "./views/GameView.vue";
import GameSessionView from "./views/GameSessionView.vue";
import Dice789View from "./views/Dice789View.vue";
import HomeView from "./views/HomeView.vue";
import MeetByChanceView from "./views/MeetByChanceView.vue";
import NotFoundView from "./views/NotFoundView.vue";
import ReplaySessionView from "./views/ReplaySessionView.vue";
import RoomView from "./views/RoomView.vue";

export const router = createRouter({
  history: createWebHistory(),
  scrollBehavior: () => ({ top: 0 }),
  routes: [
    { path: "/", name: "home", component: HomeView, meta: { title: "开始一局" } },
    { path: "/invite/:roomCode", name: "invite", component: HomeView, props: (route) => ({ inviteCode: String(route.params.roomCode ?? "") }), meta: { title: "加入房间" } },
    { path: "/room/:roomId", name: "room", component: RoomView, props: true, meta: { title: "房间" } },
    { path: "/room/:roomId/game/:sessionId", name: "game", component: GameSessionView, props: true, meta: { title: "游戏桌" } },
    { path: "/room/:roomId/replay/:sessionId", name: "replay", component: ReplaySessionView, props: true, meta: { title: "赛后复盘" } },
    { path: "/fixtures/table", name: "fixture-table", component: GameView, props: { roomId: "fixture-room", sessionId: "fixture-session", fixtureState: "active" }, meta: { title: "桌面预览" } },
    { path: "/fixtures/table/revealed", name: "fixture-table-revealed", component: GameView, props: { roomId: "fixture-room", sessionId: "fixture-session", fixtureState: "revealed" }, meta: { title: "开骰预览" } },
    { path: "/fixtures/table/spectator", name: "fixture-table-spectator", component: GameView, props: { roomId: "fixture-room", sessionId: "fixture-session", fixtureState: "spectator" }, meta: { title: "观战预览" } },
    { path: "/fixtures/table/reconnecting", name: "fixture-table-reconnecting", component: GameView, props: { roomId: "fixture-room", sessionId: "fixture-session", fixtureState: "reconnecting" }, meta: { title: "重连预览" } },
    { path: "/fixtures/table/timeout", name: "fixture-table-timeout", component: GameView, props: { roomId: "fixture-room", sessionId: "fixture-session", fixtureState: "timeout" }, meta: { title: "超时预览" } },
    { path: "/fixtures/table/replay", name: "fixture-table-replay", component: GameView, props: { roomId: "fixture-room", sessionId: "fixture-session", fixtureState: "replay" }, meta: { title: "复盘预览" } },
    { path: "/fixtures/dice-789/:fixtureState?", name: "fixture-dice-789", component: Dice789View, props: (route) => ({ fixtureState: route.params.fixtureState ?? "active" }), meta: { title: "789 预览" } },
    { path: "/fixtures/meet-by-chance/:fixtureState?", name: "fixture-meet-by-chance", component: MeetByChanceView, props: (route) => ({ fixtureState: route.params.fixtureState ?? "active" }), meta: { title: "喜相逢预览" } },
    { path: "/:pathMatch(.*)*", name: "not-found", component: NotFoundView, meta: { title: "找不到页面" } },
  ],
});

router.afterEach((to) => {
  document.title = `${String(to.meta.title ?? "Game Night")} · Game Night`;
});
