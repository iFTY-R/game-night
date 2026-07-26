<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NPopconfirm,
  NSelect,
  NSpace,
  NSpin,
  NTabPane,
  NTabs,
  NTag,
  NThing,
  useMessage,
  type FormInst,
  type FormRules,
  type SelectOption
} from "naive-ui";
import { RefreshCw, Search } from "lucide-vue-next";
import { AdminPermission } from "../../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";
import {
  AdminGameAnomalyFlag,
  AdminRepairState,
  AdminRepairType,
  AdminRoomAnomalyFlag,
  AdminRoomCommandOutcome,
  type AdminGameDetail,
  type AdminGameSummary,
  type AdminRepairOperation,
  type AdminRoomDetail,
  type AdminRoomMemberSummary,
  type AdminRoomSummary
} from "../../../../../contracts/gen/ts/platform/admin/v1/admin_room_pb";
import { GameSessionStatus } from "../../../../../contracts/gen/ts/platform/game/v1/game_pb";
import { AdmissionMode, MemberRole, RoomStatus } from "../../../../../contracts/gen/ts/platform/room/v1/room_pb";
import {
  executeEmergencyRepair,
  forceCloseRoom,
  forceTerminateGame,
  getGame,
  getRoom,
  listGames,
  listRooms,
  previewEmergencyRepair,
  removeRoomMember,
  setRoomAdmission
} from "../../api/admin-room";
import { createRequestId } from "../../api/connect";
import { AdminApiError } from "../../api/errors";
import PermissionGate from "../../components/PermissionGate.vue";
import { useAuthStore } from "../../stores/auth";
import { formatDateTime } from "../../utils/format";

const message = useMessage();
const auth = useAuthStore();

const activeTab = ref<"rooms" | "games">("rooms");
const roomForm = reactive({
  roomCode: "",
  roomId: "",
  hostUserId: "",
  memberUserId: "",
  status: 0,
  anomaliesOnly: false
});
const gameForm = reactive({
  sessionId: "",
  roomId: "",
  gameId: "",
  status: 0,
  anomaliesOnly: false
});
const admissionFormRef = ref<FormInst | null>(null);
const admissionForm = reactive({
  participantAdmission: AdmissionMode.OPEN,
  spectatorAdmission: AdmissionMode.OPEN,
  reason: ""
});
const roomActionFormRef = ref<FormInst | null>(null);
const roomActionForm = reactive({
  reason: ""
});
const gameActionFormRef = ref<FormInst | null>(null);
const gameActionForm = reactive({
  reason: ""
});
const repairFormRef = ref<FormInst | null>(null);
const repairForm = reactive({
  targetId: "",
  repairType: AdminRepairType.TERMINATE_UNRECOVERABLE_GAME,
  reason: ""
});

const rules: FormRules = {
  reason: { required: true, message: "请输入操作原因", trigger: ["input", "blur"] },
  participantAdmission: { required: true, type: "number", message: "请选择玩家入场策略", trigger: ["change"] },
  spectatorAdmission: { required: true, type: "number", message: "请选择观战入场策略", trigger: ["change"] },
  targetId: { required: true, message: "请输入修正目标 ID", trigger: ["input", "blur"] },
  repairType: { required: true, type: "number", message: "请选择修正类型", trigger: ["change"] }
};

const rooms = ref<AdminRoomSummary[]>([]);
const games = ref<AdminGameSummary[]>([]);
const selectedRoom = ref<AdminRoomDetail | null>(null);
const selectedGame = ref<AdminGameDetail | null>(null);
const selectedRepair = ref<AdminRepairOperation | null>(null);
const roomDrawerOpen = ref(false);
const gameDrawerOpen = ref(false);
const loadingRooms = ref(false);
const loadingGames = ref(false);
const loadingRoomDetail = ref(false);
const loadingGameDetail = ref(false);
const savingAdmission = ref(false);
const closingRoom = ref(false);
const removingMemberId = ref("");
const terminatingGame = ref(false);
const previewingRepair = ref(false);
const executingRepair = ref(false);
const nextRoomPageToken = ref("");
const nextGamePageToken = ref("");

type RequestLane = "rooms" | "games" | "roomDetail" | "gameDetail";

// Independent request lanes avoid cancelling the room list just because the game list refreshes in parallel.
const requestGenerations: Record<RequestLane, number> = {
  rooms: 0,
  games: 0,
  roomDetail: 0,
  gameDetail: 0
};
const activeControllers: Record<RequestLane, AbortController | null> = {
  rooms: null,
  games: null,
  roomDetail: null,
  gameDetail: null
};

const canControlRooms = computed(() => auth.permissions.includes(AdminPermission.ROOMS_CONTROL));
const canReadGames = computed(() => auth.permissions.includes(AdminPermission.GAMES_READ));
const canControlGames = computed(() => auth.permissions.includes(AdminPermission.GAMES_CONTROL));
const canRepairGames = computed(() => auth.permissions.includes(AdminPermission.GAMES_REPAIR));
const selectedRoomSummary = computed(() => selectedRoom.value?.summary ?? null);
const selectedGameSummary = computed(() => selectedGame.value?.summary ?? null);

const roomStatusOptions: SelectOption[] = [
  { label: "全部状态", value: 0 },
  { label: "大厅", value: RoomStatus.LOBBY },
  { label: "牌局中", value: RoomStatus.PLAYING },
  { label: "已关闭", value: RoomStatus.CLOSED },
  { label: "牌局后", value: RoomStatus.POST_GAME }
];
const gameStatusOptions: SelectOption[] = [
  { label: "全部状态", value: 0 },
  { label: "进行中", value: GameSessionStatus.ACTIVE },
  { label: "已暂停", value: GameSessionStatus.SUSPENDED },
  { label: "已完成", value: GameSessionStatus.FINISHED },
  { label: "已取消", value: GameSessionStatus.CANCELLED }
];
const admissionOptions: SelectOption[] = [
  { label: "开放", value: AdmissionMode.OPEN },
  { label: "审批", value: AdmissionMode.APPROVAL },
  { label: "关闭", value: AdmissionMode.CLOSED }
];
const repairTypeOptions: SelectOption[] = [
  { label: "终止不可恢复牌局", value: AdminRepairType.TERMINATE_UNRECOVERABLE_GAME },
  { label: "清理过期 Owner 租约", value: AdminRepairType.CLEAR_STALE_OWNER_LEASE },
  { label: "修复房间牌局链接", value: AdminRepairType.REPAIR_ROOM_GAME_LINK }
];

const errorMessage = (error: unknown, fallback: string): string =>
  error instanceof AdminApiError ? error.message : fallback;

const beginRequest = (lane: RequestLane): { token: number; signal: AbortSignal } => {
  requestGenerations[lane] += 1;
  activeControllers[lane]?.abort();
  activeControllers[lane] = new AbortController();
  return { token: requestGenerations[lane], signal: activeControllers[lane].signal };
};

const isCurrentRequest = (lane: RequestLane, token: number, signal: AbortSignal): boolean =>
  !signal.aborted && token === requestGenerations[lane];

const statusTagType = (status: number): "default" | "success" | "warning" | "error" => {
  if (status === RoomStatus.LOBBY || status === GameSessionStatus.ACTIVE) {
    return "success";
  }
  if (status === RoomStatus.PLAYING || status === GameSessionStatus.SUSPENDED) {
    return "warning";
  }
  if (status === RoomStatus.CLOSED || status === GameSessionStatus.CANCELLED) {
    return "error";
  }
  return "default";
};

const formatRoomStatus = (status: RoomStatus): string => {
  switch (status) {
    case RoomStatus.LOBBY:
      return "大厅";
    case RoomStatus.PLAYING:
      return "牌局中";
    case RoomStatus.CLOSED:
      return "已关闭";
    case RoomStatus.POST_GAME:
      return "牌局后";
    default:
      return "未知";
  }
};

const formatGameStatus = (status: GameSessionStatus): string => {
  switch (status) {
    case GameSessionStatus.ACTIVE:
      return "进行中";
    case GameSessionStatus.SUSPENDED:
      return "已暂停";
    case GameSessionStatus.FINISHED:
      return "已完成";
    case GameSessionStatus.CANCELLED:
      return "已取消";
    default:
      return "未知";
  }
};

const formatAdmission = (mode: AdmissionMode): string => {
  switch (mode) {
    case AdmissionMode.OPEN:
      return "开放";
    case AdmissionMode.APPROVAL:
      return "审批";
    case AdmissionMode.CLOSED:
      return "关闭";
    default:
      return "未知";
  }
};

const formatRole = (role: MemberRole): string => {
  switch (role) {
    case MemberRole.PARTICIPANT:
      return "玩家";
    case MemberRole.SPECTATOR:
      return "观众";
    case MemberRole.WAITING:
      return "等待";
    default:
      return "未知";
  }
};

const formatRoomAnomaly = (flag: AdminRoomAnomalyFlag): string => {
  switch (flag) {
    case AdminRoomAnomalyFlag.OWNER_STALE:
      return "Owner 过旧";
    case AdminRoomAnomalyFlag.OWNER_MISSING:
      return "Owner 缺失";
    case AdminRoomAnomalyFlag.ALL_PLAYERS_OFFLINE:
      return "玩家全离线";
    case AdminRoomAnomalyFlag.ROOM_GAME_LINK_MISMATCH:
      return "房间牌局链接异常";
    default:
      return "异常";
  }
};

const formatGameAnomaly = (flag: AdminGameAnomalyFlag): string => {
  switch (flag) {
    case AdminGameAnomalyFlag.OWNER_STALE:
      return "Owner 过旧";
    case AdminGameAnomalyFlag.OWNER_MISSING:
      return "Owner 缺失";
    case AdminGameAnomalyFlag.NO_RECENT_PROGRESS:
      return "长期无进展";
    case AdminGameAnomalyFlag.ROOM_LINK_MISMATCH:
      return "房间链接异常";
    default:
      return "异常";
  }
};

const formatOutcome = (outcome: AdminRoomCommandOutcome): string => {
  switch (outcome) {
    case AdminRoomCommandOutcome.EXECUTED:
      return "已执行";
    case AdminRoomCommandOutcome.NO_CHANGE:
      return "无变化";
    case AdminRoomCommandOutcome.VERSION_CONFLICT:
      return "版本冲突";
    case AdminRoomCommandOutcome.OWNER_UNREACHABLE:
      return "Owner 不可达";
    case AdminRoomCommandOutcome.REPAIR_REQUIRED:
      return "需要应急修正";
    case AdminRoomCommandOutcome.REJECTED:
      return "已拒绝";
    default:
      return "未知结果";
  }
};

const formatRepairState = (state: AdminRepairState): string => {
  switch (state) {
    case AdminRepairState.PREVIEWED:
      return "已预览";
    case AdminRepairState.EXECUTED:
      return "已执行";
    case AdminRepairState.REJECTED:
      return "已拒绝";
    case AdminRepairState.EXPIRED:
      return "已过期";
    default:
      return "未知";
  }
};

const loadRooms = async (pageToken = ""): Promise<void> => {
  const { token, signal } = beginRequest("rooms");
  loadingRooms.value = true;
  try {
    const statuses = roomForm.status ? [Number(roomForm.status) as RoomStatus] : [];
    const response = await listRooms({
      roomId: roomForm.roomId,
      roomCode: roomForm.roomCode,
      hostUserId: roomForm.hostUserId,
      memberUserId: roomForm.memberUserId,
      statuses,
      anomaliesOnly: roomForm.anomaliesOnly,
      pageSize: 20,
      pageToken,
      signal
    });
    if (!isCurrentRequest("rooms", token, signal)) {
      return;
    }
    rooms.value = pageToken ? [...rooms.value, ...response.rooms] : response.rooms;
    nextRoomPageToken.value = response.page?.nextPageToken ?? "";
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载房间列表失败。"));
    }
  } finally {
    if (isCurrentRequest("rooms", token, signal)) {
      loadingRooms.value = false;
    }
  }
};

const loadGames = async (pageToken = ""): Promise<void> => {
  if (!canReadGames.value) {
    games.value = [];
    nextGamePageToken.value = "";
    return;
  }
  const { token, signal } = beginRequest("games");
  loadingGames.value = true;
  try {
    const statuses = gameForm.status ? [Number(gameForm.status) as GameSessionStatus] : [];
    const gameIds = gameForm.gameId.trim() ? [gameForm.gameId.trim()] : [];
    const response = await listGames({
      sessionId: gameForm.sessionId,
      roomId: gameForm.roomId,
      gameIds,
      statuses,
      anomaliesOnly: gameForm.anomaliesOnly,
      pageSize: 20,
      pageToken,
      signal
    });
    if (!isCurrentRequest("games", token, signal)) {
      return;
    }
    games.value = pageToken ? [...games.value, ...response.games] : response.games;
    nextGamePageToken.value = response.page?.nextPageToken ?? "";
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载牌局列表失败。"));
    }
  } finally {
    if (isCurrentRequest("games", token, signal)) {
      loadingGames.value = false;
    }
  }
};

const loadRoomDetail = async (roomId: string): Promise<void> => {
  const { token, signal } = beginRequest("roomDetail");
  loadingRoomDetail.value = true;
  try {
    const response = await getRoom({ roomId, signal });
    if (!isCurrentRequest("roomDetail", token, signal)) {
      return;
    }
    selectedRoom.value = response.room ?? null;
    const summary = response.room?.summary;
    if (summary) {
      admissionForm.participantAdmission = summary.participantAdmission || AdmissionMode.OPEN;
      admissionForm.spectatorAdmission = summary.spectatorAdmission || AdmissionMode.OPEN;
    }
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载房间详情失败。"));
    }
  } finally {
    if (isCurrentRequest("roomDetail", token, signal)) {
      loadingRoomDetail.value = false;
    }
  }
};

const loadGameDetail = async (sessionId: string): Promise<void> => {
  const { token, signal } = beginRequest("gameDetail");
  loadingGameDetail.value = true;
  try {
    const response = await getGame({ sessionId, signal });
    if (!isCurrentRequest("gameDetail", token, signal)) {
      return;
    }
    selectedGame.value = response.game ?? null;
    repairForm.targetId = response.game?.summary?.sessionId ?? "";
  } catch (error) {
    if (!signal.aborted) {
      message.error(errorMessage(error, "加载牌局详情失败。"));
    }
  } finally {
    if (isCurrentRequest("gameDetail", token, signal)) {
      loadingGameDetail.value = false;
    }
  }
};

const openRoom = (room: AdminRoomSummary): void => {
  roomDrawerOpen.value = true;
  selectedRoom.value = null;
  selectedRepair.value = null;
  admissionForm.reason = "";
  roomActionForm.reason = "";
  void loadRoomDetail(room.roomId);
};

const openGame = (game: AdminGameSummary): void => {
  gameDrawerOpen.value = true;
  selectedGame.value = null;
  selectedRepair.value = null;
  gameActionForm.reason = "";
  repairForm.targetId = game.sessionId;
  void loadGameDetail(game.sessionId);
};

const closeRoomDrawer = (): void => {
  roomDrawerOpen.value = false;
  selectedRoom.value = null;
  selectedRepair.value = null;
  admissionForm.reason = "";
  roomActionForm.reason = "";
  admissionFormRef.value?.restoreValidation();
  roomActionFormRef.value?.restoreValidation();
};

const closeGameDrawer = (): void => {
  gameDrawerOpen.value = false;
  selectedGame.value = null;
  selectedRepair.value = null;
  gameActionForm.reason = "";
  repairForm.reason = "";
  gameActionFormRef.value?.restoreValidation();
  repairFormRef.value?.restoreValidation();
};

const upsertRoomSummary = (room?: AdminRoomSummary): void => {
  if (!room) {
    return;
  }
  rooms.value = rooms.value.map((item) => item.roomId === room.roomId ? room : item);
  if (selectedRoom.value) {
    selectedRoom.value = { ...selectedRoom.value, summary: room };
  }
};

const upsertGameSummary = (game?: AdminGameSummary): void => {
  if (!game) {
    return;
  }
  games.value = games.value.map((item) => item.sessionId === game.sessionId ? game : item);
  if (selectedGame.value) {
    selectedGame.value = { ...selectedGame.value, summary: game };
  }
};

const handleSaveAdmission = async (): Promise<void> => {
  await admissionFormRef.value?.validate();
  const summary = selectedRoomSummary.value;
  if (!summary) {
    return;
  }
  savingAdmission.value = true;
  try {
    const response = await setRoomAdmission({
      operationId: createRequestId(),
      roomId: summary.roomId,
      participantAdmission: admissionForm.participantAdmission,
      spectatorAdmission: admissionForm.spectatorAdmission,
      reason: admissionForm.reason,
      expectedRoomVersion: summary.roomVersion
    });
    upsertRoomSummary(response.room);
    admissionForm.reason = "";
    admissionFormRef.value?.restoreValidation();
    message.success(`入场策略处理完成：${formatOutcome(response.outcome)}。`);
  } catch (error) {
    message.error(errorMessage(error, "更新入场策略失败。"));
  } finally {
    savingAdmission.value = false;
  }
};

const handleRemoveMember = async (member: AdminRoomMemberSummary): Promise<void> => {
  const summary = selectedRoomSummary.value;
  if (!summary) {
    return;
  }
  if (!roomActionForm.reason.trim()) {
    message.warning("请先填写房间操作原因。");
    return;
  }
  removingMemberId.value = member.userId;
  try {
    const response = await removeRoomMember({
      operationId: createRequestId(),
      roomId: summary.roomId,
      userId: member.userId,
      reason: roomActionForm.reason,
      expectedRoomVersion: summary.roomVersion,
      expectedMembershipVersion: member.membershipVersion
    });
    upsertRoomSummary(response.room);
    selectedRoom.value = selectedRoom.value
      ? { ...selectedRoom.value, members: selectedRoom.value.members.filter((item) => item.userId !== member.userId) }
      : selectedRoom.value;
    message.success(`成员已移除：${formatOutcome(response.outcome)}，断开 ${response.revokedConnections} 条连接。`);
  } catch (error) {
    message.error(errorMessage(error, "移除成员失败。"));
  } finally {
    removingMemberId.value = "";
  }
};

const handleForceCloseRoom = async (): Promise<void> => {
  await roomActionFormRef.value?.validate();
  const summary = selectedRoomSummary.value;
  if (!summary) {
    return;
  }
  closingRoom.value = true;
  try {
    const response = await forceCloseRoom({
      operationId: createRequestId(),
      roomId: summary.roomId,
      reason: roomActionForm.reason,
      expectedRoomVersion: summary.roomVersion
    });
    upsertRoomSummary(response.room);
    message.success(`强制关房处理完成：${formatOutcome(response.outcome)}。`);
  } catch (error) {
    message.error(errorMessage(error, "强制关房失败。"));
  } finally {
    closingRoom.value = false;
  }
};

const handleForceTerminateGame = async (): Promise<void> => {
  await gameActionFormRef.value?.validate();
  const summary = selectedGameSummary.value;
  if (!summary) {
    return;
  }
  terminatingGame.value = true;
  try {
    const response = await forceTerminateGame({
      operationId: createRequestId(),
      sessionId: summary.sessionId,
      reason: gameActionForm.reason,
      expectedStateVersion: summary.stateVersion,
      expectedOwnershipEpoch: summary.ownershipEpoch
    });
    upsertGameSummary(response.game);
    message.success(`强制终止处理完成：${formatOutcome(response.outcome)}${response.repairRequired ? "，需要应急修正。" : "。"}`);
  } catch (error) {
    message.error(errorMessage(error, "强制终止牌局失败。"));
  } finally {
    terminatingGame.value = false;
  }
};

const handlePreviewRepair = async (): Promise<void> => {
  await repairFormRef.value?.validate();
  previewingRepair.value = true;
  selectedRepair.value = null;
  try {
    const response = await previewEmergencyRepair({
      targetId: repairForm.targetId,
      repairType: repairForm.repairType,
      reason: repairForm.reason
    });
    selectedRepair.value = response.repair ?? null;
    message.success("应急修正预览已生成。");
  } catch (error) {
    message.error(errorMessage(error, "预览应急修正失败。"));
  } finally {
    previewingRepair.value = false;
  }
};

const handleExecuteRepair = async (): Promise<void> => {
  const repair = selectedRepair.value;
  if (!repair) {
    return;
  }
  executingRepair.value = true;
  try {
    const response = await executeEmergencyRepair({
      operationId: createRequestId(),
      repairId: repair.repairId,
      reason: repairForm.reason,
      expectedRepairVersion: repair.repairVersion
    });
    selectedRepair.value = response.repair ?? repair;
    message.success(`应急修正已执行：${response.receipt?.auditEventId ?? "等待审计回执"}`);
    void loadGames();
    if (repair.targetId) {
      void loadGameDetail(repair.targetId);
    }
  } catch (error) {
    message.error(errorMessage(error, "执行应急修正失败。"));
  } finally {
    executingRepair.value = false;
  }
};

onMounted(() => {
  void loadRooms();
  void loadGames();
});

onBeforeUnmount(() => {
  for (const controller of Object.values(activeControllers)) {
    controller?.abort();
  }
});
</script>

<template>
  <div class="admin-view room-control">
    <header class="admin-view__header room-control__hero">
      <div>
        <p class="room-control__eyebrow">运营后台 / Rooms & Games</p>
        <h1 class="admin-view__title">房间与牌局</h1>
        <p class="admin-view__subtitle">查询线上房间与牌局状态，执行受审计的房间控制和固定应急修正。</p>
      </div>
      <NButton secondary :loading="loadingRooms || loadingGames" @click="activeTab === 'rooms' ? loadRooms() : loadGames()">
        <template #icon>
          <RefreshCw :size="16" />
        </template>
        刷新当前
      </NButton>
    </header>

    <NTabs v-model:value="activeTab" type="segment" animated class="room-control__tabs">
      <NTabPane name="rooms" tab="房间">
        <div class="room-control__grid">
          <NCard :bordered="false" class="room-control__panel">
            <template #header>
              <span class="room-control__panel-title">房间检索</span>
            </template>
            <NForm :model="roomForm" label-placement="top">
              <NFormItem label="房间码">
                <NInput v-model:value="roomForm.roomCode" clearable placeholder="输入房间码" @keyup.enter="loadRooms()" />
              </NFormItem>
              <NFormItem label="房间 ID">
                <NInput v-model:value="roomForm.roomId" clearable placeholder="精确房间 ID" @keyup.enter="loadRooms()" />
              </NFormItem>
              <NFormItem label="房主用户 ID">
                <NInput v-model:value="roomForm.hostUserId" clearable placeholder="按房主过滤" @keyup.enter="loadRooms()" />
              </NFormItem>
              <NFormItem label="成员用户 ID">
                <NInput v-model:value="roomForm.memberUserId" clearable placeholder="按成员过滤" @keyup.enter="loadRooms()" />
              </NFormItem>
              <NFormItem label="状态">
                <NSelect v-model:value="roomForm.status" :options="roomStatusOptions" />
              </NFormItem>
              <NCheckbox v-model:checked="roomForm.anomaliesOnly">只看异常房间</NCheckbox>
              <NButton type="primary" block :loading="loadingRooms" class="room-control__search" @click="loadRooms()">
                <template #icon>
                  <Search :size="16" />
                </template>
                查询房间
              </NButton>
            </NForm>
          </NCard>

          <NCard :bordered="false" class="room-control__list">
            <template #header>
              <span class="room-control__panel-title">房间列表</span>
            </template>
            <NSpin :show="loadingRooms">
              <div v-if="rooms.length" class="entity-list">
                <button v-for="room in rooms" :key="room.roomId" class="entity-list__item" type="button" @click="openRoom(room)">
                  <div class="entity-list__main">
                    <div class="entity-list__topline">
                      <strong>{{ room.roomCode || room.roomId }}</strong>
                      <NTag size="small" :type="statusTagType(room.status)">{{ formatRoomStatus(room.status) }}</NTag>
                      <NTag v-if="room.anomalies.length" size="small" type="error">{{ room.anomalies.length }} 个异常</NTag>
                    </div>
                    <div class="entity-list__meta">
                      <span>房主 {{ room.hostUsername || room.hostUserId || "未知" }}</span>
                      <span>玩家 {{ room.participantCount }}</span>
                      <span>观众 {{ room.spectatorCount }}</span>
                      <span>版本 {{ room.roomVersion }}</span>
                    </div>
                    <div class="entity-list__tags">
                      <NTag v-for="flag in room.anomalies" :key="flag" size="small" type="error">{{ formatRoomAnomaly(flag) }}</NTag>
                      <span v-if="!room.anomalies.length">未发现异常</span>
                    </div>
                  </div>
                  <div class="entity-list__time">
                    <span>最近活动</span>
                    <strong>{{ formatDateTime(room.lastActivityAt) }}</strong>
                  </div>
                </button>
                <NButton v-if="nextRoomPageToken" secondary block :loading="loadingRooms" @click="loadRooms(nextRoomPageToken)">加载更多房间</NButton>
              </div>
              <NEmpty v-else description="暂无匹配房间" />
            </NSpin>
          </NCard>
        </div>
      </NTabPane>

      <NTabPane name="games" tab="牌局" :disabled="!canReadGames">
        <PermissionGate :permission="AdminPermission.GAMES_READ">
          <div class="room-control__grid">
            <NCard :bordered="false" class="room-control__panel">
              <template #header>
                <span class="room-control__panel-title">牌局检索</span>
              </template>
              <NForm :model="gameForm" label-placement="top">
                <NFormItem label="会话 ID">
                  <NInput v-model:value="gameForm.sessionId" clearable placeholder="精确牌局会话 ID" @keyup.enter="loadGames()" />
                </NFormItem>
                <NFormItem label="房间 ID">
                  <NInput v-model:value="gameForm.roomId" clearable placeholder="按房间过滤" @keyup.enter="loadGames()" />
                </NFormItem>
                <NFormItem label="游戏 ID">
                  <NInput v-model:value="gameForm.gameId" clearable placeholder="按游戏类型过滤" @keyup.enter="loadGames()" />
                </NFormItem>
                <NFormItem label="状态">
                  <NSelect v-model:value="gameForm.status" :options="gameStatusOptions" />
                </NFormItem>
                <NCheckbox v-model:checked="gameForm.anomaliesOnly">只看异常牌局</NCheckbox>
                <NButton type="primary" block :loading="loadingGames" class="room-control__search" @click="loadGames()">
                  <template #icon>
                    <Search :size="16" />
                  </template>
                  查询牌局
                </NButton>
              </NForm>
            </NCard>

            <NCard :bordered="false" class="room-control__list">
              <template #header>
                <span class="room-control__panel-title">牌局列表</span>
              </template>
              <NSpin :show="loadingGames">
                <div v-if="games.length" class="entity-list">
                  <button v-for="game in games" :key="game.sessionId" class="entity-list__item" type="button" @click="openGame(game)">
                    <div class="entity-list__main">
                      <div class="entity-list__topline">
                        <strong>{{ game.roomCode || game.roomId }}</strong>
                        <NTag size="small" :type="statusTagType(game.status)">{{ formatGameStatus(game.status) }}</NTag>
                        <NTag v-if="game.anomalies.length" size="small" type="error">{{ game.anomalies.length }} 个异常</NTag>
                      </div>
                      <div class="entity-list__meta">
                        <span>会话 {{ game.sessionId }}</span>
                        <span>游戏 {{ game.gameId || "未知" }}</span>
                        <span>状态版本 {{ game.stateVersion }}</span>
                      </div>
                      <div class="entity-list__tags">
                        <NTag v-for="flag in game.anomalies" :key="flag" size="small" type="error">{{ formatGameAnomaly(flag) }}</NTag>
                        <span v-if="!game.anomalies.length">未发现异常</span>
                      </div>
                    </div>
                    <div class="entity-list__time">
                      <span>最近进展</span>
                      <strong>{{ formatDateTime(game.lastProgressAt) }}</strong>
                    </div>
                  </button>
                  <NButton v-if="nextGamePageToken" secondary block :loading="loadingGames" @click="loadGames(nextGamePageToken)">加载更多牌局</NButton>
                </div>
                <NEmpty v-else description="暂无匹配牌局" />
              </NSpin>
            </NCard>
          </div>
          <template #fallback>
            <NEmpty description="当前管理员无牌局读取权限" />
          </template>
        </PermissionGate>
      </NTabPane>
    </NTabs>

    <NDrawer :show="roomDrawerOpen" width="780" placement="right" @update:show="(value) => !value && closeRoomDrawer()">
      <NDrawerContent :title="selectedRoomSummary?.roomCode || '房间详情'" closable>
        <NSpin :show="loadingRoomDetail">
          <NSpace v-if="selectedRoomSummary" vertical size="large">
            <NDescriptions bordered :column="2" label-placement="left">
              <NDescriptionsItem label="房间 ID">{{ selectedRoomSummary.roomId }}</NDescriptionsItem>
              <NDescriptionsItem label="状态">
                <NTag :type="statusTagType(selectedRoomSummary.status)">{{ formatRoomStatus(selectedRoomSummary.status) }}</NTag>
              </NDescriptionsItem>
              <NDescriptionsItem label="活跃牌局">{{ selectedRoomSummary.activeSessionId || selectedRoomSummary.activeGameId || "无" }}</NDescriptionsItem>
              <NDescriptionsItem label="房主">{{ selectedRoomSummary.hostUsername || selectedRoomSummary.hostUserId || "未知" }}</NDescriptionsItem>
              <NDescriptionsItem label="入场策略">玩家 {{ formatAdmission(selectedRoomSummary.participantAdmission) }} / 观战 {{ formatAdmission(selectedRoomSummary.spectatorAdmission) }}</NDescriptionsItem>
              <NDescriptionsItem label="版本">房间 {{ selectedRoomSummary.roomVersion }} / 成员 {{ selectedRoomSummary.membershipVersion }}</NDescriptionsItem>
              <NDescriptionsItem label="Owner Epoch">{{ selectedRoomSummary.ownershipEpoch }}</NDescriptionsItem>
              <NDescriptionsItem label="最近活动">{{ formatDateTime(selectedRoomSummary.lastActivityAt) }}</NDescriptionsItem>
            </NDescriptions>

            <NAlert v-if="selectedRoomSummary.anomalies.length" type="warning" :bordered="false">
              异常：{{ selectedRoomSummary.anomalies.map(formatRoomAnomaly).join("、") }}
            </NAlert>

            <PermissionGate :permission="AdminPermission.ROOMS_CONTROL">
              <NCard title="房间控制" size="small" :bordered="false">
                <NSpace vertical>
                  <NForm ref="admissionFormRef" :model="admissionForm" :rules="rules" label-placement="top">
                    <NFormItem label="玩家入场" path="participantAdmission">
                      <NSelect v-model:value="admissionForm.participantAdmission" :options="admissionOptions" />
                    </NFormItem>
                    <NFormItem label="观战入场" path="spectatorAdmission">
                      <NSelect v-model:value="admissionForm.spectatorAdmission" :options="admissionOptions" />
                    </NFormItem>
                    <NFormItem label="操作原因" path="reason">
                      <NInput v-model:value="admissionForm.reason" type="textarea" placeholder="说明调整入场策略的原因" />
                    </NFormItem>
                    <NButton type="primary" :loading="savingAdmission" @click="handleSaveAdmission">保存入场策略</NButton>
                  </NForm>

                  <NForm ref="roomActionFormRef" :model="roomActionForm" :rules="rules" label-placement="top">
                    <NFormItem label="房间操作原因" path="reason">
                      <NInput v-model:value="roomActionForm.reason" type="textarea" placeholder="移除成员或强制关房前必须填写" />
                    </NFormItem>
                    <NPopconfirm @positive-click="handleForceCloseRoom">
                      <template #trigger>
                        <NButton type="error" secondary :loading="closingRoom">强制关闭房间</NButton>
                      </template>
                      该操作会关闭房间并写入审计记录，确认继续？
                    </NPopconfirm>
                  </NForm>
                </NSpace>
              </NCard>
            </PermissionGate>

            <NCard title="成员" size="small" :bordered="false">
              <div class="room-control__members">
                <div v-for="member in selectedRoom?.members ?? []" :key="member.userId" class="room-control__member">
                  <div>
                    <strong>{{ member.username || member.userId }}</strong>
                    <p>{{ formatRole(member.role) }} / 申请 {{ formatRole(member.requestedRole) }} · 成员版本 {{ member.membershipVersion }}</p>
                  </div>
                  <NSpace align="center">
                    <NTag size="small" :type="member.online ? 'success' : 'default'">{{ member.online ? "在线" : "离线" }}</NTag>
                    <PermissionGate :permission="AdminPermission.ROOMS_CONTROL">
                      <NPopconfirm @positive-click="handleRemoveMember(member)">
                        <template #trigger>
                          <NButton size="small" type="error" ghost :loading="removingMemberId === member.userId">移除</NButton>
                        </template>
                        将按当前成员版本移除该成员，确认继续？
                      </NPopconfirm>
                    </PermissionGate>
                  </NSpace>
                </div>
                <NEmpty v-if="!(selectedRoom?.members.length)" description="暂无成员" />
              </div>
            </NCard>

            <NCard title="近期房间事件" size="small" :bordered="false">
              <NThing v-for="event in selectedRoom?.recentEvents ?? []" :key="event.eventId" :title="event.eventType || event.eventId">
                <template #description>{{ formatDateTime(event.occurredAt) }} · {{ event.actorUserId || "系统" }}</template>
                <p>{{ event.digest || "无摘要" }}</p>
              </NThing>
              <NEmpty v-if="!(selectedRoom?.recentEvents.length)" description="暂无事件" />
            </NCard>
          </NSpace>
          <NEmpty v-else description="请选择房间" />
        </NSpin>
      </NDrawerContent>
    </NDrawer>

    <NDrawer :show="gameDrawerOpen" width="780" placement="right" @update:show="(value) => !value && closeGameDrawer()">
      <NDrawerContent :title="selectedGameSummary?.sessionId || '牌局详情'" closable>
        <NSpin :show="loadingGameDetail">
          <NSpace v-if="selectedGameSummary" vertical size="large">
            <NDescriptions bordered :column="2" label-placement="left">
              <NDescriptionsItem label="会话 ID">{{ selectedGameSummary.sessionId }}</NDescriptionsItem>
              <NDescriptionsItem label="状态">
                <NTag :type="statusTagType(selectedGameSummary.status)">{{ formatGameStatus(selectedGameSummary.status) }}</NTag>
              </NDescriptionsItem>
              <NDescriptionsItem label="房间">{{ selectedGameSummary.roomCode || selectedGameSummary.roomId }}</NDescriptionsItem>
              <NDescriptionsItem label="游戏">{{ selectedGameSummary.gameId }} / {{ selectedGameSummary.gameVersion || "未提供" }}</NDescriptionsItem>
              <NDescriptionsItem label="状态版本">{{ selectedGameSummary.stateVersion }}</NDescriptionsItem>
              <NDescriptionsItem label="Owner Epoch">{{ selectedGameSummary.ownershipEpoch }}</NDescriptionsItem>
              <NDescriptionsItem label="开始时间">{{ formatDateTime(selectedGameSummary.startedAt) }}</NDescriptionsItem>
              <NDescriptionsItem label="最近进展">{{ formatDateTime(selectedGameSummary.lastProgressAt) }}</NDescriptionsItem>
            </NDescriptions>

            <NAlert v-if="selectedGameSummary.anomalies.length" type="warning" :bordered="false">
              异常：{{ selectedGameSummary.anomalies.map(formatGameAnomaly).join("、") }}
            </NAlert>

            <PermissionGate :permission="AdminPermission.GAMES_CONTROL">
              <NCard title="牌局控制" size="small" :bordered="false">
                <NForm ref="gameActionFormRef" :model="gameActionForm" :rules="rules" label-placement="top">
                  <NFormItem label="操作原因" path="reason">
                    <NInput v-model:value="gameActionForm.reason" type="textarea" placeholder="说明强制终止牌局的原因" />
                  </NFormItem>
                  <NPopconfirm @positive-click="handleForceTerminateGame">
                    <template #trigger>
                      <NButton type="error" secondary :loading="terminatingGame">强制终止牌局</NButton>
                    </template>
                    将按当前状态版本和 Owner Epoch 尝试终止牌局，确认继续？
                  </NPopconfirm>
                </NForm>
              </NCard>
            </PermissionGate>

            <PermissionGate :permission="AdminPermission.GAMES_REPAIR">
              <NCard title="固定应急修正" size="small" :bordered="false">
                <NAlert type="warning" :bordered="false" class="room-control__repair-alert">
                  应急修正只支持后端白名单命令，不允许任意数据库补丁；执行前必须先生成预览。
                </NAlert>
                <NForm ref="repairFormRef" :model="repairForm" :rules="rules" label-placement="top">
                  <NFormItem label="目标 ID" path="targetId">
                    <NInput v-model:value="repairForm.targetId" placeholder="通常为牌局 sessionId" />
                  </NFormItem>
                  <NFormItem label="修正类型" path="repairType">
                    <NSelect v-model:value="repairForm.repairType" :options="repairTypeOptions" />
                  </NFormItem>
                  <NFormItem label="原因" path="reason">
                    <NInput v-model:value="repairForm.reason" type="textarea" placeholder="说明为什么进入应急修正流程" />
                  </NFormItem>
                  <NSpace>
                    <NButton type="warning" :loading="previewingRepair" @click="handlePreviewRepair">预览修正</NButton>
                    <NPopconfirm :disabled="!selectedRepair" @positive-click="handleExecuteRepair">
                      <template #trigger>
                        <NButton type="error" :disabled="!selectedRepair" :loading="executingRepair">执行预览</NButton>
                      </template>
                      执行后可能产生不可逆影响，确认继续？
                    </NPopconfirm>
                  </NSpace>
                </NForm>
                <NDescriptions v-if="selectedRepair" bordered :column="1" label-placement="left" class="room-control__repair">
                  <NDescriptionsItem label="修正 ID">{{ selectedRepair.repairId }}</NDescriptionsItem>
                  <NDescriptionsItem label="状态">{{ formatRepairState(selectedRepair.state) }}</NDescriptionsItem>
                  <NDescriptionsItem label="摘要">{{ selectedRepair.summary || "无摘要" }}</NDescriptionsItem>
                  <NDescriptionsItem label="预览摘要">{{ selectedRepair.previewDigest }}</NDescriptionsItem>
                  <NDescriptionsItem label="修正版本">{{ selectedRepair.repairVersion }}</NDescriptionsItem>
                  <NDescriptionsItem label="不可逆影响">{{ selectedRepair.irreversibleEffects.join("、") || "未声明" }}</NDescriptionsItem>
                </NDescriptions>
              </NCard>
            </PermissionGate>

            <NCard title="参与者" size="small" :bordered="false">
              <div class="room-control__members">
                <div v-for="participant in selectedGame?.participants ?? []" :key="participant.userId" class="room-control__member">
                  <div>
                    <strong>{{ participant.username || participant.userId }}</strong>
                    <p>{{ formatRole(participant.roomRole) }}</p>
                  </div>
                  <NTag size="small" :type="participant.active ? 'success' : 'default'">{{ participant.active ? "活跃" : "离场" }}</NTag>
                </div>
                <NEmpty v-if="!(selectedGame?.participants.length)" description="暂无参与者" />
              </div>
            </NCard>

            <NCard title="近期牌局事件" size="small" :bordered="false">
              <NThing v-for="event in selectedGame?.recentEvents ?? []" :key="event.eventId" :title="event.eventType || event.eventId">
                <template #description>{{ formatDateTime(event.occurredAt) }} · 版本 {{ event.stateVersion }} · {{ event.actorUserId || "系统" }}</template>
                <p>{{ event.digest || "无摘要" }}</p>
              </NThing>
              <NEmpty v-if="!(selectedGame?.recentEvents.length)" description="暂无事件" />
            </NCard>
          </NSpace>
          <NEmpty v-else description="请选择牌局" />
        </NSpin>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>

<style scoped>
.room-control {
  --room-card: rgba(255, 255, 255, 0.88);
  --room-ink: #152235;
  --room-line: rgba(30, 41, 59, 0.12);
  --room-muted: #64748b;
}

.room-control__hero {
  align-items: flex-end;
  background:
    radial-gradient(circle at top right, rgba(245, 158, 11, 0.2), transparent 30rem),
    linear-gradient(135deg, rgba(15, 23, 42, 0.04), rgba(20, 184, 166, 0.12));
  border: 1px solid var(--room-line);
  border-radius: 28px;
  display: flex;
  justify-content: space-between;
  padding: 24px;
}

.room-control__eyebrow {
  color: #0f766e;
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.14em;
  margin: 0 0 8px;
  text-transform: uppercase;
}

.room-control__tabs {
  margin-top: 18px;
}

.room-control__grid {
  display: grid;
  gap: 18px;
  grid-template-columns: minmax(280px, 340px) minmax(0, 1fr);
}

.room-control__panel,
.room-control__list {
  background: var(--room-card);
  border-radius: 22px;
  box-shadow: 0 24px 70px rgba(15, 23, 42, 0.08);
}

.room-control__panel-title {
  color: var(--room-ink);
  font-weight: 800;
}

.room-control__search,
.room-control__repair-alert,
.room-control__repair {
  margin-top: 16px;
}

.entity-list {
  display: grid;
  gap: 12px;
}

.entity-list__item {
  align-items: center;
  background: linear-gradient(135deg, rgba(255, 255, 255, 0.94), rgba(248, 250, 252, 0.9));
  border: 1px solid var(--room-line);
  border-radius: 18px;
  color: inherit;
  cursor: pointer;
  display: grid;
  gap: 14px;
  grid-template-columns: minmax(0, 1fr) minmax(150px, auto);
  padding: 14px;
  text-align: left;
  transition: border-color 0.18s ease, box-shadow 0.18s ease, transform 0.18s ease;
  width: 100%;
}

.entity-list__item:hover {
  border-color: rgba(20, 184, 166, 0.48);
  box-shadow: 0 14px 34px rgba(20, 184, 166, 0.12);
  transform: translateY(-1px);
}

.entity-list__main {
  min-width: 0;
}

.entity-list__topline,
.entity-list__meta,
.entity-list__tags {
  align-items: center;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.entity-list__topline strong {
  color: var(--room-ink);
  font-size: 15px;
}

.entity-list__meta {
  color: var(--room-muted);
  font-size: 12px;
  margin: 4px 0 8px;
}

.entity-list__tags span {
  color: var(--room-muted);
  font-size: 12px;
}

.entity-list__time {
  color: var(--room-muted);
  display: grid;
  font-size: 12px;
  gap: 4px;
  justify-items: end;
}

.entity-list__time strong {
  color: var(--room-ink);
  font-weight: 600;
}

.room-control__members {
  display: grid;
  gap: 10px;
}

.room-control__member {
  align-items: center;
  border: 1px solid var(--room-line);
  border-radius: 14px;
  display: flex;
  justify-content: space-between;
  padding: 12px;
}

.room-control__member p {
  color: var(--room-muted);
  font-size: 12px;
  margin: 4px 0 0;
}

@media (max-width: 900px) {
  .room-control__hero {
    align-items: flex-start;
    flex-direction: column;
    gap: 16px;
  }

  .room-control__grid {
    grid-template-columns: 1fr;
  }

  .entity-list__item {
    grid-template-columns: 1fr;
  }

  .entity-list__time {
    justify-items: start;
  }
}
</style>
