<script setup lang="ts">
import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import {
  ConfigSchema as Dice789ConfigSchema,
  ContinueMode,
  DICE_789_SCHEMA_VERSION,
  DICE_789_VERSION,
  type Config as Dice789Config,
} from "@game-night/dice-789-client";
import {
  ConfigSchema as LiarsDiceConfigSchema,
  LIARS_DICE_SCHEMA_VERSION,
  LIARS_DICE_VERSION,
  type Config as LiarsDiceConfig,
} from "@game-night/liars-dice-client";
import {
  ConfigSchema as MeetByChanceConfigSchema,
  MEET_BY_CHANCE_SCHEMA_VERSION,
  MEET_BY_CHANCE_VERSION,
  type Config as MeetByChanceConfig,
} from "@game-night/meet-by-chance-client";
import {
  ConfigSchema as ThreeRoundsConfigSchema,
  THREE_ROUNDS_SCHEMA_VERSION,
  THREE_ROUNDS_VERSION,
  defaultThreeRoundsConfig,
  type Config as ThreeRoundsConfig,
} from "@game-night/three-rounds-client";
import { ActionTray, DangerConfirm, GameTable, type TableSeat, type TrayState } from "@game-night/game-ui-kit";
import { ArrowLeft, Check, ChevronDown, Clock3, DoorClosed, History, LockKeyhole, Play, Save, Share2, SlidersHorizontal, Trash2, UserMinus, UserPlus, Users, X } from "lucide-vue-next";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";

import { gameClient, type GameEnvelopeWire, type GameRulePresetWire, type PendingGameStartWire, type RoomGameConfigDraftWire, type ReplayAccessPolicy, type ReplayAccessWire, type RoomMember, type RoomSnapshot } from "../api/client";
import ProfileTrigger from "../components/ProfileTrigger.vue";
import UsernameDialog, { type UsernameChangedEvent, type UsernameDialogHandle } from "../components/UsernameDialog.vue";
import { useRoomStore } from "../stores/room";
import { gameById, gameCatalog, isGameId, type GameId } from "../game-catalog";
import { memberDisplayName } from "../member-display";
import { useRoomPresenceLease } from "../composables/use-room-presence-lease";

const props = defineProps<{ roomId: string }>();
const route = useRoute();
const router = useRouter();
const room = useRoomStore();
useRoomPresenceLease(() => props.roomId);
const shared = ref(false);
const entryOpen = ref(true);
const loading = ref(true);
const actionError = ref("");
const profileSyncNotice = ref("");
const usernameDialog = ref<UsernameDialogHandle | null>(null);
const selectedGameId = ref<GameId>("liars-dice");
const replayAccess = ref<ReplayAccessWire | null>(null);
const replayAccessLoading = ref(false);
const replayAccessSaving = ref(false);
const ruleTrayState = ref<TrayState>("collapsed");
const ruleSaving = ref(false);
const presetsLoading = ref(false);
const presetName = ref("");
const rulePresets = ref<GameRulePresetWire[]>([]);
const startSaving = ref(false);
const countdownCancelSaving = ref(false);
const nowMs = ref(Date.now());
const isLandscape = ref(false);
type GovernanceConfirmation = { kind: "remove"; userId: string } | { kind: "close" };
const governanceConfirmation = ref<GovernanceConfirmation | null>(null);
const governanceSaving = ref(false);
let governanceTrigger: HTMLElement | null = null;
let refreshTimer: number | undefined;
let clockTimer: number | undefined;
let orientationQuery: MediaQueryList | undefined;
const syncLandscape = (): void => {
  isLandscape.value = orientationQuery?.matches ?? false;
};
let refreshPending = false;
let gameSelectionInitialized = false;
let roomTerminated = false;
let autoStartPendingId = "";
const SESSION_CONFIG_MESSAGE = "session.config";
const quickPaceSeconds = 20;
const tablePaceSeconds = 30;
const latePaceSeconds = 45;
const softPenaltyTicks = 2;
const classicPenaltyTicks = 4;
const spicyPenaltyTicks = 6;
type RulePace = "quick" | "table" | "late";
type RuleStakes = "soft" | "classic" | "spicy";
interface RuleTuning {
  pace: RulePace;
  stakes: RuleStakes;
  variant: boolean;
  roundOneTimeoutSeconds: number;
  roundTwoTimeoutSeconds: number;
  roundResultSeconds: number;
  finalResultSeconds: number;
}
type RoomSnapshotRules = RoomSnapshot & {
  selectedGameId?: string;
  gameConfigDrafts?: RoomGameConfigDraftWire[];
  pendingStart?: PendingGameStartWire;
  ownershipEpoch?: string;
};
type RoomRuleStore = ReturnType<typeof useRoomStore> & {
  selectGame?: (gameId: string) => Promise<RoomSnapshot | null> | RoomSnapshot | null | void;
  selectRemoteGame?: (gameId: string) => Promise<RoomSnapshot | null> | RoomSnapshot | null | void;
  updateGameConfig?: (gameId: string, config: GameEnvelopeWire, expectedRevision?: string | number | bigint) => Promise<unknown> | unknown;
  updateRemoteGameConfig?: (gameId: string, config: GameEnvelopeWire, expectedRevision?: string | number | bigint) => Promise<unknown> | unknown;
  loadGameRulePresets?: (gameId: string) => Promise<GameRulePresetWire[] | { presets?: GameRulePresetWire[] }> | GameRulePresetWire[] | { presets?: GameRulePresetWire[] };
  listGameRulePresets?: (gameId: string) => Promise<GameRulePresetWire[] | { presets?: GameRulePresetWire[] }> | GameRulePresetWire[] | { presets?: GameRulePresetWire[] };
  createGameRulePreset?: (gameId: string, name: string, config: GameEnvelopeWire) => Promise<unknown> | unknown;
  updateGameRulePreset?: (preset: GameRulePresetWire, config: GameEnvelopeWire, name?: string) => Promise<unknown> | unknown;
  deleteGameRulePreset?: (preset: GameRulePresetWire) => Promise<unknown> | unknown;
  saveGameRulePreset?: (input: Record<string, unknown>) => Promise<unknown> | unknown;
  deleteGameRulePresetById?: (presetId: string, expectedRevision: string | number | bigint) => Promise<unknown> | unknown;
  beginGameStart?: (gameId: string, configRevision?: string | number | bigint) => Promise<unknown> | unknown;
  beginRemoteGameStart?: (gameId: string, configRevision?: string | number | bigint) => Promise<unknown> | unknown;
  cancelGameStart?: (pendingStart?: PendingGameStartWire) => Promise<unknown> | unknown;
  cancelRemoteGameStart?: (pendingStart?: PendingGameStartWire | null) => Promise<unknown> | unknown;
};
const roomRules = room as RoomRuleStore;
const threeRoundsDefaults = defaultThreeRoundsConfig();
const defaultRuleTuning = (): RuleTuning => ({
  pace: "table",
  stakes: "classic",
  variant: true,
  roundOneTimeoutSeconds: threeRoundsDefaults.roundOneTimeoutSeconds,
  roundTwoTimeoutSeconds: threeRoundsDefaults.roundTwoTimeoutSeconds,
  roundResultSeconds: threeRoundsDefaults.roundResultSeconds,
  finalResultSeconds: threeRoundsDefaults.finalResultSeconds,
});
const ruleTuning = ref<RuleTuning>(defaultRuleTuning());
const roomCode = computed(() => room.roomCode ?? props.roomId.toUpperCase().slice(0, 6));
const remoteRoom = computed(() => room.remoteRoom);
const remoteRuleRoom = computed(() => remoteRoom.value as RoomSnapshotRules | null);
const isRemote = computed(() => remoteRoom.value !== null);
const roomStatus = computed(() => remoteRoom.value?.status ?? "ROOM_STATUS_LOBBY");
const isPlaying = computed(() => roomStatus.value.includes("PLAYING"));
const isPostGame = computed(() => roomStatus.value.includes("POST_GAME"));
const profileAvailable = computed(() => remoteRoom.value !== null && !isPlaying.value);
// A table exit sets this query flag so hosts can reach governance controls without being auto-routed back into the game.
const stayInRoom = computed(() => route.query.manage === "1");
const currentHost = computed(() => remoteRoom.value?.hostUserId === room.userId);
const members = computed(() => remoteRoom.value?.members ?? []);
const displayMembers = computed(() => members.value);
const displayHostUserId = computed(() => remoteRoom.value?.hostUserId ?? "");
const participantMembers = computed(() =>
  [...displayMembers.value]
    .filter((member) => member.role.includes("PARTICIPANT"))
    .sort((left, right) => left.seatIndex - right.seatIndex || left.userId.localeCompare(right.userId)),
);
const waitingMembers = computed(() => displayMembers.value.filter((member) => member.role.includes("WAITING")));
const spectatorMembers = computed(() => displayMembers.value.filter((member) => member.role.includes("SPECTATOR")));
const participantCount = computed(() => participantMembers.value.length);
const currentMember = computed(() => displayMembers.value.find((member) => member.userId === room.userId));
const canEnterActiveGame = computed(() => currentMember.value?.role.includes("PARTICIPANT") || currentMember.value?.role.includes("SPECTATOR"));
const viewerCanSeeRules = computed(() => currentMember.value !== undefined);
const selectedGame = computed(() => gameById(selectedGameId.value) ?? gameCatalog[0]);
const activeGame = computed(() => gameById(remoteRoom.value?.activeGameId ?? ""));
const selectedDraft = computed(() => remoteRuleRoom.value?.gameConfigDrafts?.find((draft) => draft.gameId === selectedGameId.value));
const pendingStart = computed(() => remoteRuleRoom.value?.pendingStart ?? null);
const pendingStartGame = computed(() => gameById(pendingStart.value?.gameId ?? "") ?? selectedGame.value);
const pendingStartDeadlineMs = computed(() => timestampMs(pendingStart.value?.deadline));
const pendingStartRemainingMs = computed(() => {
  const deadline = pendingStartDeadlineMs.value;
  return deadline === null ? 0 : Math.max(0, deadline - nowMs.value);
});
const pendingStartSeconds = computed(() => Math.ceil(pendingStartRemainingMs.value / 1000));
const hasActivePendingStart = computed(() => pendingStart.value !== null && (pendingStartDeadlineMs.value === null || pendingStartRemainingMs.value > 0));
const newRuleFlowAvailable = computed(() => typeof roomRules.beginGameStart === "function");
const enoughPlayers = computed(() => participantCount.value >= selectedGame.value.minimumPlayers);
const ruleSummary = computed(() => summaryFromEnvelope(selectedDraft.value?.config, selectedGameId.value) ?? summaryFromTuning(selectedGameId.value, ruleTuning.value));
const traySummary = computed(() => ruleSummary.value.slice(0, 2));
const ruleTrayHint = computed(() => currentHost.value ? "由你配置并同步给全房成员" : viewerCanSeeRules.value ? "房主已锁定这局规则" : "加入房间后可查看完整规则");
/** Only seated participants belong on the shared table; waiting and spectators stay in supporting lists. */
const tableSeats = computed<readonly TableSeat[]>(() =>
  participantMembers.value.map((member) => ({
    seatIndex: member.seatIndex,
    userId: member.userId,
    displayName: displayMemberName(member.userId),
    avatarText: displayMemberName(member.userId).slice(0, 1),
    status: memberStatusLabel(member),
    connected: true,
    host: member.userId === displayHostUserId.value,
  })),
);
const tableSelfSeatIndex = computed(() => {
  const selfSeat = participantMembers.value.find((member) => member.userId === room.userId);
  return selfSeat?.seatIndex ?? participantMembers.value[0]?.seatIndex ?? 0;
});
/** Portrait tables trade label width for a clear shared center; landscape keeps the full seat treatment. */
const tableSeatWidth = computed(() => isLandscape.value ? 118 : 92);
/** The portrait tray overlaps the lower stage, so seat layout keeps a matching safety band clear of seat cards. */
const tableBottomInset = computed(() => {
  if (isPlaying.value || isLandscape.value) return 0;
  if (ruleTrayState.value === "expanded") return 344;
  if (ruleTrayState.value === "compact") return 204;
  return 82;
});
const startButtonLabel = computed(() => {
  if (!enoughPlayers.value) return `还需 ${selectedGame.value.minimumPlayers - participantCount.value} 人`;
  if (hasActivePendingStart.value) return `${pendingStartGame.value.name} ${pendingStartSeconds.value || "即将"} 秒后开局`;
  if (newRuleFlowAvailable.value) return isPostGame.value ? "准备再开一局" : `准备开始${selectedGame.value.name}`;
  return isPostGame.value ? "再开一局" : `开始${selectedGame.value.name}`;
});
/** The visible label stays scannable inside the table while aria-label preserves the full action context. */
const startButtonText = computed(() => {
  if (!enoughPlayers.value) return `差 ${selectedGame.value.minimumPlayers - participantCount.value} 人`;
  if (hasActivePendingStart.value) return `${pendingStartSeconds.value || "即将"} 秒`;
  return isPostGame.value ? "再来" : "开始";
});
const displayMemberName = (userId: string): string => {
  const member = displayMembers.value.find((candidate) => candidate.userId === userId);
  return memberDisplayName(userId, member?.username);
};
const memberStatusLabel = (member: RoomMember): string => {
  if (member.role.includes("WAITING")) return "候场中";
  if (member.role.includes("SPECTATOR")) return "观战";
  return member.userId === displayHostUserId.value ? "房主 · 已入座" : "已入座";
};
const governanceTitle = computed(() => governanceConfirmation.value?.kind === "remove" ? "确认移出成员？" : "确认解散房间？");
const governanceDescription = computed(() => {
  const confirmation = governanceConfirmation.value;
  if (confirmation?.kind === "remove") {
    const effect = isPlaying.value ? "对局中的冻结座位会保留，并由游戏规则接管离场处理。" : "对方将立即失去这个房间的访问权限。";
    return `${displayMemberName(confirmation.userId)}将被移出。${effect}`;
  }
  const sessionEffect = isPlaying.value ? "当前对局会立即终止；取消前已公开的进度会保留，未公开手牌继续保密。" : "";
  return `${sessionEffect}房间码会立即失效，所有成员都需要返回发现页。这项操作无法撤销。`;
});
// Public replay is a valid choice only for a public room; private-room clients never offer an invalid widening command.
const replayPolicyOptions = computed<readonly { value: ReplayAccessPolicy; label: string }[]>(() => [
  { value: "REPLAY_ACCESS_POLICY_PARTICIPANT", label: "仅本局玩家" },
  { value: "REPLAY_ACCESS_POLICY_ROOM_MEMBER", label: "本局结束时的房间成员" },
  ...(remoteRoom.value?.visibility.includes("PUBLIC")
    ? [{ value: "REPLAY_ACCESS_POLICY_PUBLIC" as const, label: "所有已登录用户" }]
    : []),
]);

const requestedGame = typeof route.query.game === "string" ? route.query.game : "";
if (isGameId(requestedGame)) {
  selectedGameId.value = requestedGame;
  gameSelectionInitialized = true;
}

/** Normalizes the server-selected table game without trusting unauthenticated viewers with rule payloads. */
const selectedGameFromSnapshot = (snapshot: RoomSnapshotRules): string => snapshot.selectedGameId || (snapshot.status.includes("POST_GAME") ? snapshot.lastFinishedGameId : snapshot.activeGameId);

/** Seeds the next game from room history once without overwriting a host choice during polling. */
const initializeGameSelection = (snapshot: NonNullable<typeof room.remoteRoom>): void => {
  if (gameSelectionInitialized) return;
  const rememberedGame = selectedGameFromSnapshot(snapshot as RoomSnapshotRules);
  if (isGameId(rememberedGame)) selectedGameId.value = rememberedGame;
  gameSelectionInitialized = true;
  syncTuningFromDraft();
};

/** Host edits call the upcoming store action when it exists; members only mirror the selected room state. */
const selectGame = async (gameId: GameId): Promise<void> => {
  selectedGameId.value = gameId;
  gameSelectionInitialized = true;
  syncTuningFromDraft();
  const selectAction = roomRules.selectRemoteGame ?? roomRules.selectGame;
  if (isRemote.value && currentHost.value && !isPlaying.value && !hasActivePendingStart.value && typeof selectAction === "function") {
    await runRuleAction(() => selectAction(gameId), "游戏选择更新失败");
  }
  void loadRulePresets();
};

const paceSeconds = (pace: RulePace): number => pace === "quick" ? quickPaceSeconds : pace === "late" ? latePaceSeconds : tablePaceSeconds;
const stakesTicks = (stakes: RuleStakes): number => stakes === "soft" ? softPenaltyTicks : stakes === "spicy" ? spicyPenaltyTicks : classicPenaltyTicks;
const paceFromSeconds = (seconds: number): RulePace => seconds <= quickPaceSeconds ? "quick" : seconds >= latePaceSeconds ? "late" : "table";
const stakesFromTicks = (ticks: number): RuleStakes => ticks <= softPenaltyTicks ? "soft" : ticks >= spicyPenaltyTicks ? "spicy" : "classic";

/** Encodes the host-visible controls into each game module's protobuf config envelope. */
const bytesToBase64 = (bytes: Uint8Array): string => {
  let value = "";
  for (const byte of bytes) value += String.fromCharCode(byte);
  return btoa(value);
};

const buildConfigEnvelope = (gameId: GameId, tuning: RuleTuning): GameEnvelopeWire => {
  const actionTimeoutSeconds = paceSeconds(tuning.pace);
  const penaltyTicks = stakesTicks(tuning.stakes);
  if (gameId === "three-rounds") {
    const config = create(ThreeRoundsConfigSchema, {
      roundOneTimeoutSeconds: tuning.roundOneTimeoutSeconds,
      roundTwoTimeoutSeconds: tuning.roundTwoTimeoutSeconds,
      roundResultSeconds: tuning.roundResultSeconds,
      finalResultSeconds: tuning.finalResultSeconds,
    });
    return { gameId, version: THREE_ROUNDS_VERSION, schemaVersion: THREE_ROUNDS_SCHEMA_VERSION, messageType: SESSION_CONFIG_MESSAGE, payload: bytesToBase64(toBinary(ThreeRoundsConfigSchema, config)) };
  }
  if (gameId === "dice-789") {
    const config = create(Dice789ConfigSchema, {
      initialPoolTicks: tuning.stakes === "soft" ? 0 : tuning.stakes === "spicy" ? 4 : 2,
      layerCapacityTicks: 8,
      addStepTicks: 2,
      maxLayers: tuning.variant ? 3 : 1,
      stackedPool: tuning.variant,
      ordinaryPairsReverse: true,
      doubleOneEnabled: true,
      doubleFourEnabled: true,
      doubleSixEnabled: true,
      continueMode: ContinueMode.OPTIONAL,
      lastDigitMatch: tuning.variant,
      actionTimeoutSeconds,
      dropReportWindowSeconds: 5,
    });
    return { gameId, version: DICE_789_VERSION, schemaVersion: DICE_789_SCHEMA_VERSION, messageType: SESSION_CONFIG_MESSAGE, payload: bytesToBase64(toBinary(Dice789ConfigSchema, config)) };
  }
  if (gameId === "meet-by-chance") {
    const config = create(MeetByChanceConfigSchema, {
      straight123: true,
      straight234: true,
      straight345: true,
      straight456: true,
      special235Enabled: tuning.variant,
      onesWild: tuning.variant,
      targetPenaltyTicks: tuning.stakes === "soft" ? 1 : tuning.stakes === "spicy" ? 3 : 2,
      rerollPenaltyTicks: tuning.stakes === "soft" ? 1 : tuning.stakes === "spicy" ? 3 : 2,
      matchPenaltyTicks: penaltyTicks,
      weakExtraPenaltyTicks: tuning.stakes === "spicy" ? 3 : 2,
      targetRerollLimit: tuning.variant ? 2 : 1,
      matchResolutionLimit: tuning.variant ? 3 : 2,
      actionTimeoutSeconds,
    });
    return { gameId, version: MEET_BY_CHANCE_VERSION, schemaVersion: MEET_BY_CHANCE_SCHEMA_VERSION, messageType: SESSION_CONFIG_MESSAGE, payload: bytesToBase64(toBinary(MeetByChanceConfigSchema, config)) };
  }
  const config = create(LiarsDiceConfigSchema, {
    dicePerPlayer: tuning.variant ? 5 : 4,
    onesWild: tuning.variant,
    strictEnabled: true,
    flyingEnabled: tuning.variant,
    firstBidMinimum: tuning.stakes === "soft" ? 3 : tuning.stakes === "spicy" ? 5 : 4,
    penaltyTicks,
    actionTimeoutSeconds,
  });
  return { gameId, version: LIARS_DICE_VERSION, schemaVersion: LIARS_DICE_SCHEMA_VERSION, messageType: SESSION_CONFIG_MESSAGE, payload: bytesToBase64(toBinary(LiarsDiceConfigSchema, config)) };
};

const bytesFromPayload = (payload: GameEnvelopeWire["payload"] | undefined): Uint8Array | null => {
  if (payload instanceof Uint8Array) return payload;
  if (Array.isArray(payload)) return Uint8Array.from(payload);
  if (typeof payload !== "string" || payload.length === 0) return null;
  try {
    const binary = atob(payload);
    return Uint8Array.from(binary, (character) => character.charCodeAt(0));
  } catch {
    return null;
  }
};

const tuningFromEnvelope = (envelope: GameEnvelopeWire | undefined, fallbackGameId: GameId): RuleTuning | null => {
  const bytes = bytesFromPayload(envelope?.payload);
  const candidateGameId = envelope?.gameId;
  const gameId: GameId = candidateGameId !== undefined && isGameId(candidateGameId) ? candidateGameId : fallbackGameId;
  if (bytes === null) return null;
  try {
    if (gameId === "three-rounds") {
      const config = fromBinary(ThreeRoundsConfigSchema, bytes);
      return {
        ...defaultRuleTuning(),
        roundOneTimeoutSeconds: config.roundOneTimeoutSeconds,
        roundTwoTimeoutSeconds: config.roundTwoTimeoutSeconds,
        roundResultSeconds: config.roundResultSeconds,
        finalResultSeconds: config.finalResultSeconds,
      };
    }
    if (gameId === "dice-789") {
      const config = fromBinary(Dice789ConfigSchema, bytes);
      return { ...defaultRuleTuning(), pace: paceFromSeconds(config.actionTimeoutSeconds), stakes: config.initialPoolTicks >= 4 ? "spicy" : config.initialPoolTicks === 0 ? "soft" : "classic", variant: config.stackedPool || config.lastDigitMatch };
    }
    if (gameId === "meet-by-chance") {
      const config = fromBinary(MeetByChanceConfigSchema, bytes);
      return { ...defaultRuleTuning(), pace: paceFromSeconds(config.actionTimeoutSeconds), stakes: stakesFromTicks(config.matchPenaltyTicks), variant: config.special235Enabled || config.onesWild };
    }
    const config = fromBinary(LiarsDiceConfigSchema, bytes);
    return { ...defaultRuleTuning(), pace: paceFromSeconds(config.actionTimeoutSeconds), stakes: stakesFromTicks(config.penaltyTicks), variant: config.onesWild || config.flyingEnabled };
  } catch {
    return null;
  }
};

const summaryFromTuning = (gameId: GameId, tuning: RuleTuning): string[] => {
  if (gameId === "three-rounds") {
    return [
      `第一关 ${tuning.roundOneTimeoutSeconds} 秒`,
      `第二关 ${tuning.roundTwoTimeoutSeconds} 秒`,
      `结果停留 ${tuning.roundResultSeconds} 秒`,
      `总榜停留 ${tuning.finalResultSeconds} 秒`,
    ];
  }
  const timeout = `${paceSeconds(tuning.pace)} 秒行动时限`;
  const stakes = tuning.stakes === "soft" ? "轻罚" : tuning.stakes === "spicy" ? "重罚" : "标准惩罚";
  if (gameId === "dice-789") return [timeout, stakes, tuning.variant ? "叠杯池与尾数规则开启" : "单层杯池"];
  if (gameId === "meet-by-chance") return [timeout, stakes, tuning.variant ? "235 与 1 点万能开启" : "基础顺子规则"];
  return [timeout, stakes, tuning.variant ? "1 点万能与飞斋开启" : "四骰基础局"];
};

const summaryFromEnvelope = (envelope: GameEnvelopeWire | undefined, fallbackGameId: GameId): string[] | null => {
  const bytes = bytesFromPayload(envelope?.payload);
  const candidateGameId = envelope?.gameId;
  const gameId: GameId = candidateGameId !== undefined && isGameId(candidateGameId) ? candidateGameId : fallbackGameId;
  if (bytes === null) return null;
  try {
    if (gameId === "three-rounds") {
      const config: ThreeRoundsConfig = fromBinary(ThreeRoundsConfigSchema, bytes);
      return [
        `第一关 ${config.roundOneTimeoutSeconds} 秒`,
        `第二关 ${config.roundTwoTimeoutSeconds} 秒`,
        `结果停留 ${config.roundResultSeconds} 秒`,
        `总榜停留 ${config.finalResultSeconds} 秒`,
      ];
    }
    if (gameId === "dice-789") {
      const config: Dice789Config = fromBinary(Dice789ConfigSchema, bytes);
      return [`${config.actionTimeoutSeconds} 秒行动时限`, `初始杯池 ${config.initialPoolTicks} 格`, config.stackedPool ? `最多 ${config.maxLayers} 层叠杯池` : "单层杯池"];
    }
    if (gameId === "meet-by-chance") {
      const config: MeetByChanceConfig = fromBinary(MeetByChanceConfigSchema, bytes);
      return [`${config.actionTimeoutSeconds} 秒行动时限`, `喜相逢罚 ${config.matchPenaltyTicks} 格`, config.special235Enabled ? "235 特例开启" : "235 特例关闭"];
    }
    const config: LiarsDiceConfig = fromBinary(LiarsDiceConfigSchema, bytes);
    return [`每人 ${config.dicePerPlayer} 颗骰`, `${config.actionTimeoutSeconds} 秒行动时限`, config.onesWild ? "1 点万能" : "1 点按普通点数"];
  } catch {
    return null;
  }
};

/** Keeps local sliders aligned after polling, preset application, or remote host edits. */
const syncTuningFromDraft = (): void => {
  const next = tuningFromEnvelope(selectedDraft.value?.config, selectedGameId.value);
  ruleTuning.value = next ?? defaultRuleTuning();
};

const runRuleAction = async (action: () => Promise<unknown> | unknown, fallback: string): Promise<void> => {
  actionError.value = "";
  try {
    await action();
    await refreshRoom();
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : fallback;
  }
};

const saveRuleConfig = async (): Promise<void> => {
  if (ruleSaving.value || !currentHost.value || isPlaying.value || hasActivePendingStart.value) return;
  ruleSaving.value = true;
  const config = buildConfigEnvelope(selectedGameId.value, ruleTuning.value);
  try {
    const updateAction = roomRules.updateRemoteGameConfig ?? roomRules.updateGameConfig;
    if (typeof updateAction === "function") {
      await runRuleAction(() => updateAction(selectedGameId.value, config, selectedDraft.value?.revision), "规则更新失败");
    }
  } finally {
    ruleSaving.value = false;
  }
};

const normalizePresetList = (response: GameRulePresetWire[] | { presets?: GameRulePresetWire[] } | undefined): GameRulePresetWire[] =>
  Array.isArray(response) ? response : response?.presets ?? [];

/** Presets are personal host resources; members only receive the immutable room draft summary. */
const loadRulePresets = async (): Promise<void> => {
  const listAction = roomRules.loadGameRulePresets ?? roomRules.listGameRulePresets;
  if (!currentHost.value || typeof listAction !== "function") {
    rulePresets.value = [];
    return;
  }
  presetsLoading.value = true;
  try {
    rulePresets.value = normalizePresetList(await listAction(selectedGameId.value));
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "规则预设加载失败";
  } finally {
    presetsLoading.value = false;
  }
};

const createRulePreset = async (): Promise<void> => {
  const name = presetName.value.trim();
  if (!name || typeof roomRules.saveGameRulePreset !== "function") return;
  await runRuleAction(() => roomRules.saveGameRulePreset?.({
    gameId: selectedGameId.value,
    name: name.slice(0, 32),
    config: buildConfigEnvelope(selectedGameId.value, ruleTuning.value),
    mode: "GAME_RULE_PRESET_WRITE_MODE_CREATE",
  }), "规则预设保存失败");
  presetName.value = "";
  await loadRulePresets();
};

const applyRulePreset = async (preset: GameRulePresetWire): Promise<void> => {
  const updateAction = roomRules.updateRemoteGameConfig ?? roomRules.updateGameConfig;
  const config = preset.config;
  if (config === undefined || typeof updateAction !== "function" || !isGameId(preset.gameId)) return;
  selectedGameId.value = preset.gameId;
  const next = tuningFromEnvelope(config, preset.gameId);
  if (next !== null) ruleTuning.value = next;
  await runRuleAction(() => updateAction(preset.gameId, config, selectedDraft.value?.revision), "规则预设应用失败");
};

const updateRulePreset = async (preset: GameRulePresetWire): Promise<void> => {
  if (typeof roomRules.saveGameRulePreset !== "function") return;
  await runRuleAction(() => roomRules.saveGameRulePreset?.({
    presetId: preset.presetId,
    gameId: preset.gameId,
    name: preset.name,
    config: buildConfigEnvelope(selectedGameId.value, ruleTuning.value),
    mode: "GAME_RULE_PRESET_WRITE_MODE_OVERWRITE",
    expectedPresetRevision: preset.presetRevision,
  }), "规则预设覆盖失败");
  await loadRulePresets();
};

const deleteRulePreset = async (preset: GameRulePresetWire): Promise<void> => {
  const deleteAction = roomRules.deleteGameRulePresetById ?? roomRules.deleteGameRulePreset;
  if (typeof deleteAction !== "function") return;
  await runRuleAction(() => deleteAction(preset.presetId, preset.presetRevision), "规则预设删除失败");
  await loadRulePresets();
};

const timestampMs = (value: unknown): number | null => {
  if (value instanceof Date) return value.getTime();
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    return Number.isFinite(parsed) ? parsed : null;
  }
  if (typeof value === "object" && value !== null) {
    const candidate = value as { seconds?: string | number | bigint; nanos?: number };
    const seconds = typeof candidate.seconds === "bigint" ? Number(candidate.seconds) : Number(candidate.seconds ?? NaN);
    if (Number.isFinite(seconds)) return seconds * 1000 + Math.floor((candidate.nanos ?? 0) / 1_000_000);
  }
  return null;
};

if (room.roomId !== props.roomId) {
  room.enterRoom(props.roomId, roomCode.value);
}

/** Loads policy only for the current host and terminal session; the replay endpoint remains independently authorized. */
const loadReplayAccess = async (snapshot: RoomSnapshot): Promise<void> => {
  const sessionId = snapshot.lastFinishedSessionId;
  if (!snapshot.status.includes("POST_GAME") || snapshot.hostUserId !== room.userId || !sessionId) {
    replayAccess.value = null;
    return;
  }
  if (replayAccessLoading.value || replayAccess.value?.sessionId === sessionId) return;
  replayAccessLoading.value = true;
  try {
    const response = await gameClient.getReplayAccess(snapshot.roomId, sessionId);
    if (response.access?.roomId !== snapshot.roomId || response.access.sessionId !== sessionId) {
      throw new Error("复盘权限响应与上一局不匹配");
    }
    replayAccess.value = response.access;
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "复盘权限加载失败";
  } finally {
    replayAccessLoading.value = false;
  }
};

/** Stops the fallback poll before terminal navigation or component teardown. */
const stopRefreshPolling = (): void => {
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer);
  refreshTimer = undefined;
};

/** Treats CLOSED as a terminal state so members never remain inside an invalid room shell. */
const exitClosedRoom = async (snapshot: RoomSnapshot): Promise<boolean> => {
  if (!snapshot.status.includes("CLOSED")) return false;
  roomTerminated = true;
  stopRefreshPolling();
  const message = snapshot.hostUserId === room.userId ? "房间已解散" : "房主已解散房间";
  room.exitRoom(message);
  await router.replace({ name: "home" });
  return true;
};

/** Refreshes lobby state so remote starts and admission changes appear without reloading. */
const refreshRoom = async (): Promise<void> => {
  if (roomTerminated || refreshPending || document.visibilityState === "hidden") return;
  refreshPending = true;
  try {
    const loaded = await room.loadRoom(props.roomId);
    if (loaded) {
      if (await exitClosedRoom(loaded)) return;
      entryOpen.value = !loaded.participantAdmission.includes("CLOSED");
      initializeGameSelection(loaded);
      if (gameSelectionInitialized) syncTuningFromDraft();
      void loadReplayAccess(loaded);
      if (!stayInRoom.value && loaded.status.includes("PLAYING") && loaded.activeSessionId && canEnterActiveGame.value) void enterActiveGame();
    }
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "房间加载失败";
  } finally {
    refreshPending = false;
  }
};

/** Rehydrates every username projection independently after the identity transaction has already committed. */
const handleUsernameChanged = async (_event: UsernameChangedEvent): Promise<void> => {
  profileSyncNotice.value = "";
  const results = await Promise.allSettled([
    room.loadRoom(props.roomId),
    room.loadMyRooms(true),
    room.loadPublicRooms(true),
  ]);
  const currentRoomResult = results[0];
  if (currentRoomResult.status === "fulfilled" && currentRoomResult.value !== null) {
    const snapshot = currentRoomResult.value;
    if (await exitClosedRoom(snapshot)) return;
    entryOpen.value = !snapshot.participantAdmission.includes("CLOSED");
    initializeGameSelection(snapshot);
    syncTuningFromDraft();
  }
  if (results.some((result) => result.status === "rejected")) {
    profileSyncNotice.value = "用户名已更新，部分房间信息同步失败，可稍后刷新";
  }
};

onMounted(async () => {
  orientationQuery = window.matchMedia("(orientation: landscape)");
  syncLandscape();
  orientationQuery.addEventListener("change", syncLandscape);
  if (room.remoteRoom?.roomId === props.roomId) {
    if (await exitClosedRoom(room.remoteRoom)) {
      loading.value = false;
      return;
    }
    entryOpen.value = !room.remoteRoom.participantAdmission.includes("CLOSED");
    initializeGameSelection(room.remoteRoom);
    syncTuningFromDraft();
    void loadReplayAccess(room.remoteRoom);
    if (!stayInRoom.value && room.remoteRoom.status.includes("PLAYING") && room.remoteRoom.activeSessionId && canEnterActiveGame.value) void enterActiveGame();
  } else {
    await refreshRoom();
  }
  loading.value = false;
  if (!roomTerminated) {
    refreshTimer = window.setInterval(() => { void refreshRoom(); }, 2_500);
    clockTimer = window.setInterval(() => { nowMs.value = Date.now(); }, 250);
  }
  void loadRulePresets();
});

onBeforeUnmount(() => {
  stopRefreshPolling();
  if (clockTimer !== undefined) window.clearInterval(clockTimer);
  orientationQuery?.removeEventListener("change", syncLandscape);
});

watch([selectedDraft, currentHost], () => {
  syncTuningFromDraft();
  void loadRulePresets();
});

watch(() => remoteRuleRoom.value?.selectedGameId, (serverGameId) => {
  if (!serverGameId || !isGameId(serverGameId) || selectedGameId.value === serverGameId) return;
  // Members learn the host's table choice from the authoritative room snapshot;
  // hosts also recover here when a stale selection write is rejected server-side.
  selectedGameId.value = serverGameId;
  gameSelectionInitialized = true;
  syncTuningFromDraft();
});

watch(hasActivePendingStart, (active) => {
  if (active) ruleTrayState.value = "collapsed";
});

/** Uses the platform share sheet on mobile and copies the same deep link everywhere else. */
const shareRoom = async (): Promise<void> => {
  const url = new URL(`/invite/${encodeURIComponent(roomCode.value)}`, window.location.origin).toString();
  try {
    if (navigator.share) {
      await navigator.share({ title: "加入 Game Night 房间", text: `房间码 ${roomCode.value}`, url });
    } else {
      await navigator.clipboard.writeText(url);
    }
    shared.value = true;
    window.setTimeout(() => { shared.value = false; }, 1200);
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") return;
    actionError.value = "分享失败，请稍后重试";
  }
};

const startGame = async (): Promise<void> => {
  if (!remoteRoom.value || !currentHost.value || startSaving.value || hasActivePendingStart.value) return;
  actionError.value = "";
  startSaving.value = true;
  try {
    const beginAction = roomRules.beginRemoteGameStart ?? roomRules.beginGameStart;
    if (pendingStart.value === null && typeof beginAction === "function") {
      await beginAction(selectedGameId.value, selectedDraft.value?.revision);
      await refreshRoom();
      return;
    }
    const response = await room.startRemoteGame(selectedGameId.value);
    const sessionId = response.sessionId;
    if (!sessionId) throw new Error("开局响应缺少会话编号");
    room.setSession(sessionId);
    await router.push({ name: "game", params: { roomId: room.roomId ?? props.roomId, sessionId } });
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "开局失败";
  } finally {
    startSaving.value = false;
  }
};

/** Cancels only the current server-bound countdown, leaving room membership and rule drafts intact. */
const cancelGameStart = async (): Promise<void> => {
  const cancelAction = roomRules.cancelRemoteGameStart ?? roomRules.cancelGameStart;
  if (!currentHost.value || countdownCancelSaving.value || typeof cancelAction !== "function") return;
  countdownCancelSaving.value = true;
  try {
    await runRuleAction(() => cancelAction(pendingStart.value ?? null), "开局倒计时取消失败");
  } finally {
    countdownCancelSaving.value = false;
  }
};

watch([pendingStart, pendingStartRemainingMs, currentHost], ([pending, remaining, host]) => {
  const pendingID = pending?.pendingStartId ?? "";
  if (!host || !pendingID || pendingStartDeadlineMs.value === null || remaining > 0 || autoStartPendingId === pendingID) return;
  // The server deadline gates StartGame; the host client submits once it reaches
  // zero so every member transitions without requiring a second tap.
  autoStartPendingId = pendingID;
  void startGame();
});

/** Re-enters the current authoritative session without creating another game. */
const enterActiveGame = async (): Promise<void> => {
  const sessionId = remoteRoom.value?.activeSessionId;
  if (!sessionId || !canEnterActiveGame.value) return;
  room.setSession(sessionId);
  await router.push({ name: "game", params: { roomId: props.roomId, sessionId } });
};

/** Opens the immutable last-session projection; authorization remains enforced by the replay API. */
const openLastReplay = async (): Promise<void> => {
  const sessionId = remoteRoom.value?.lastFinishedSessionId;
  if (!sessionId) return;
  await router.push({ name: "replay", params: { roomId: props.roomId, sessionId } });
};

/** Saves one allowed policy and reloads the authoritative value after any CAS conflict or transport failure. */
const changeReplayPolicy = async (event: Event): Promise<void> => {
  const select = event.target as HTMLSelectElement;
  const requested = select.value as ReplayAccessPolicy;
  const current = replayAccess.value;
  if (!current || replayAccessSaving.value || !replayPolicyOptions.value.some((option) => option.value === requested)) {
    if (current) select.value = current.policy;
    return;
  }
  replayAccessSaving.value = true;
  actionError.value = "";
  let reload = false;
  try {
    const response = await gameClient.setReplayAccess(props.roomId, current.sessionId, requested, current.policyVersion);
    if (response.access?.roomId !== props.roomId || response.access.sessionId !== current.sessionId) {
      throw new Error("复盘权限更新响应不完整");
    }
    replayAccess.value = response.access;
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "复盘权限更新失败";
    select.value = current.policy;
    replayAccess.value = null;
    reload = true;
  } finally {
    replayAccessSaving.value = false;
  }
  if (reload && remoteRoom.value) await loadReplayAccess(remoteRoom.value);
};

const toggleAdmission = async (): Promise<void> => {
  if (!remoteRoom.value || !currentHost.value) return;
  const nextOpen = !entryOpen.value;
  entryOpen.value = nextOpen;
  try {
    await room.setAdmissionRemote(nextOpen ? "ADMISSION_MODE_OPEN" : "ADMISSION_MODE_CLOSED", "ADMISSION_MODE_OPEN");
  } catch (error) {
    entryOpen.value = !nextOpen;
    actionError.value = error instanceof Error ? error.message : "更新进房许可失败";
  }
};

const approveMember = async (userId: string): Promise<void> => {
  try {
    await room.approveRemoteMember(userId);
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : "候场晋升失败";
  }
};

const toggleRuleTray = (): void => {
  ruleTrayState.value = ruleTrayState.value === "expanded" ? "compact" : "expanded";
};

/** Opens one shared destructive confirmation and places keyboard focus on the safe action. */
const openGovernanceConfirmation = async (confirmation: GovernanceConfirmation, event: Event): Promise<void> => {
  if (governanceSaving.value) return;
  actionError.value = "";
  governanceTrigger = event.currentTarget instanceof HTMLElement ? event.currentTarget : null;
  governanceConfirmation.value = confirmation;
  await nextTick();
};

const requestRemoveMember = (member: RoomMember, event: Event): Promise<void> =>
  openGovernanceConfirmation({ kind: "remove", userId: member.userId }, event);

const requestCloseRoom = (event: Event): Promise<void> => openGovernanceConfirmation({ kind: "close" }, event);

/** Cancels a pending destructive command and restores focus to the control that opened it. */
const cancelGovernanceConfirmation = async (): Promise<void> => {
  if (governanceSaving.value) return;
  governanceConfirmation.value = null;
  await nextTick();
  governanceTrigger?.focus();
  governanceTrigger = null;
};

/** Commits the confirmed command once; conflicts keep the dialog open and refresh the authoritative room. */
const confirmGovernance = async (): Promise<void> => {
  const confirmation = governanceConfirmation.value;
  if (!confirmation || governanceSaving.value) return;
  governanceSaving.value = true;
  actionError.value = "";
  let roomClosed = false;
  try {
    if (confirmation.kind === "remove") {
      const updated = await room.removeRemoteMember(confirmation.userId);
      if (!updated) throw new Error("成员移出响应不完整");
    } else {
      const updated = await room.closeRemoteRoom();
      if (!updated?.status.includes("CLOSED")) throw new Error("房间解散响应不完整");
      roomClosed = true;
    }
    governanceConfirmation.value = null;
  } catch (error) {
    actionError.value = error instanceof Error ? error.message : confirmation.kind === "remove" ? "成员移出失败" : "房间解散失败";
    await refreshRoom();
  } finally {
    governanceSaving.value = false;
  }
  if (roomClosed) {
    room.exitRoom("房间已解散");
    await router.replace({ name: "home" });
  }
};

const leave = async (): Promise<void> => {
  room.leaveRoom();
  await router.push({ name: "home" });
};
</script>

<template>
  <main class="screen-shell room-screen">
    <header class="topbar">
      <button class="icon-button" type="button" title="离开房间" @click="leave"><ArrowLeft :size="21" aria-hidden="true" /></button>
      <div v-if="remoteRoom" class="room-code">
        <span>房间码</span>
        <strong>{{ roomCode }}</strong>
        <button type="button" :title="shared ? '已分享' : '分享房间链接'" @click="shareRoom">
          <Check v-if="shared" :size="17" aria-hidden="true" />
          <Share2 v-else :size="17" aria-hidden="true" />
        </button>
      </div>
      <div v-if="remoteRoom" class="room-topbar__tools">
        <span class="room-count"><Users :size="16" aria-hidden="true" /> {{ participantCount }} / {{ remoteRoom.participantCapacity }}</span>
        <ProfileTrigger v-if="profileAvailable" :username="room.displayName" @activate="usernameDialog?.open('profile')" />
      </div>
    </header>

    <section v-if="loading" class="room-hero" aria-labelledby="room-loading-title">
      <p class="eyebrow">正在连接</p>
      <h1 id="room-loading-title" class="display-title">正在同步房间状态。</h1>
      <p class="loading-note" role="status">正在验证身份、成员与当前游戏桌…</p>
    </section>

    <section v-else-if="!remoteRoom" class="room-hero" aria-labelledby="room-unavailable-title">
      <p class="eyebrow">无法进入</p>
      <h1 id="room-unavailable-title" class="display-title">这个房间暂时无法打开。</h1>
      <p class="form-error" role="alert">{{ actionError || "房间不存在，或当前设备没有访问权限。" }}</p>
      <button class="button button--quiet room-hero__enter" type="button" @click="leave"><ArrowLeft :size="18" aria-hidden="true" /> 返回发现页</button>
    </section>

    <template v-else>
    <section class="room-hero">
      <p class="eyebrow">{{ isPlaying ? (activeGame?.name ?? "正在游戏") : isPostGame ? "上一局结束" : selectedGame.name + " · 等候区" }}</p>
      <h1 class="display-title">{{ isPlaying ? "这一局正在进行。" : isPostGame ? "要不要再开一局？" : "朋友到齐，再开骰盅。" }}</h1>
      <p class="muted">开局后新玩家会在本局结束前等候。每局结束，房主可以重新开放进房许可。</p>
      <button v-if="isPlaying && canEnterActiveGame" class="button room-hero__enter" type="button" @click="enterActiveGame">
        <Play :size="18" fill="currentColor" aria-hidden="true" /> 进入{{ activeGame?.name ?? "当前游戏" }}
      </button>
      <button v-if="isPostGame && remoteRoom?.lastFinishedSessionId" class="button button--quiet room-hero__enter" type="button" @click="openLastReplay">
        <History :size="18" aria-hidden="true" /> 查看上一局复盘
      </button>
      <p v-if="actionError" class="form-error" role="alert">{{ actionError }}</p>
      <p v-if="profileSyncNotice" class="profile-sync-notice" role="status">{{ profileSyncNotice }}</p>
    </section>

    <section class="room-stage panel" aria-labelledby="players-title">
      <div class="room-stage__head">
        <div><p class="eyebrow">共同桌面</p><h2 id="players-title" class="section-title">桌边的人</h2></div>
        <span class="entry-status" :class="{ 'is-closed': !entryOpen }">
          <component :is="entryOpen ? Users : LockKeyhole" :size="15" aria-hidden="true" />
          {{ entryOpen ? "允许进房" : "暂停进房" }}
        </span>
      </div>
      <div class="room-stage__table-shell">
        <div class="room-stage__table">
          <GameTable
            v-if="tableSeats.length > 0"
            :seats="tableSeats"
            :self-seat-index="tableSelfSeatIndex"
            :seat-width="tableSeatWidth"
            :bottom-inset="tableBottomInset"
            shape="elongated-oval"
            label="房间桌面"
          >
            <template #center>
              <section
                class="room-center-card"
                :class="{ 'is-tray-compact': ruleTrayState === 'compact', 'is-tray-expanded': ruleTrayState === 'expanded' }"
                aria-label="本局玩法与开局"
              >
                <p class="eyebrow">{{ isPostGame ? "上一局结束" : "本局玩法" }}</p>
                <div class="room-center-card__heading">
                  <strong>{{ selectedGame.name }}</strong>
                </div>
                <ul v-if="!hasActivePendingStart && (viewerCanSeeRules || currentHost)" class="room-center-card__summary">
                  <li v-for="item in traySummary" :key="item">{{ item }}</li>
                </ul>
                <p v-else-if="!hasActivePendingStart" class="room-center-card__summary is-redacted">加入后可查看规则。</p>
                <div class="room-center-card__actions">
                  <button
                    class="room-center-card__rules"
                    type="button"
                    :title="ruleTrayState === 'expanded' ? '收起完整规则' : '展开完整规则'"
                    :aria-label="ruleTrayState === 'expanded' ? '收起完整规则' : `查看${selectedGame.name}完整规则`"
                    @click="toggleRuleTray"
                  >
                    <SlidersHorizontal :size="19" aria-hidden="true" />
                  </button>
                  <button
                    v-if="!hasActivePendingStart && (currentHost || !isRemote)"
                    class="button room-center-card__start"
                    type="button"
                    :aria-label="startButtonLabel"
                    :disabled="startSaving || !enoughPlayers || (isRemote && !currentHost)"
                    @click="startGame"
                  >
                    <Play :size="19" fill="currentColor" aria-hidden="true" /> {{ startButtonText }}
                  </button>
                </div>
                <section v-if="hasActivePendingStart" class="countdown-card room-center-card__countdown" role="status" aria-live="polite">
                  <Clock3 :size="18" aria-hidden="true" />
                  <div>
                    <strong>{{ pendingStartSeconds || "即将" }}</strong>
                    <span>{{ pendingStartGame.name }} 正在倒计时开局</span>
                  </div>
                  <button v-if="currentHost" class="danger-control" type="button" :disabled="countdownCancelSaving" @click="cancelGameStart">取消开局</button>
                </section>
              </section>
            </template>
          </GameTable>
          <div v-else class="room-stage__empty">
            <Users :size="24" aria-hidden="true" />
            <p>成员入座后会围绕桌面自动排布。</p>
          </div>
        </div>

        <aside v-if="!isPlaying" class="room-stage__rules" aria-labelledby="game-picker-title">
          <ActionTray v-model="ruleTrayState" :pending="ruleSaving || presetsLoading || startSaving || countdownCancelSaving" label="规则与开局">
            <template #summary>
              <div class="rule-tray__summary">
                <div class="rule-tray__summary-copy">
                  <strong>{{ selectedGame.name }}</strong>
                  <span>{{ ruleTrayHint }}</span>
                </div>
                <small v-if="selectedDraft?.revision">规则 #{{ selectedDraft.revision }}</small>
              </div>
            </template>
            <template #primary>
              <section v-if="viewerCanSeeRules || currentHost" class="rule-summary rule-summary--tray" aria-label="当前规则摘要">
                <p class="eyebrow">规则摘要</p>
                <ul>
                  <li v-for="item in traySummary" :key="item">{{ item }}</li>
                </ul>
              </section>
              <section v-else class="rule-redacted" aria-label="规则已隐藏">
                <LockKeyhole :size="18" aria-hidden="true" />
                <p>你还不是房间成员，规则细节已隐藏。</p>
              </section>
            </template>
            <template #details>
              <div class="rule-panel__body">
                <div class="game-picker__head">
                  <div><p class="eyebrow">本局玩法</p><h2 id="game-picker-title" class="section-title">规则与开局</h2></div>
                  <span>{{ currentHost ? "由你配置" : viewerCanSeeRules ? "房主已锁定规则" : "加入后可见" }}</span>
                </div>

                <div v-if="viewerCanSeeRules || currentHost" class="game-options">
                  <button
                    v-for="game in gameCatalog"
                    :key="game.gameId"
                    class="game-option"
                    :class="{ 'is-selected': selectedGameId === game.gameId }"
                    type="button"
                    :aria-pressed="selectedGameId === game.gameId"
                    :disabled="isRemote && (!currentHost || hasActivePendingStart)"
                    @click="selectGame(game.gameId)"
                  >
                    <span>{{ game.accent }}</span>
                    <strong>{{ game.name }}</strong>
                    <small>{{ game.summary }}</small>
                    <em>至少 {{ game.minimumPlayers }} 人</em>
                  </button>
                </div>

                <section v-if="currentHost" class="rule-controls" aria-label="规则配置">
                  <template v-if="selectedGameId === 'three-rounds'">
                    <label>
                      <span>第一关时限</span>
                      <input v-model.number="ruleTuning.roundOneTimeoutSeconds" type="number" min="5" max="180" :disabled="ruleSaving || hasActivePendingStart" @change="saveRuleConfig">
                    </label>
                    <label>
                      <span>第二关时限</span>
                      <input v-model.number="ruleTuning.roundTwoTimeoutSeconds" type="number" min="5" max="180" :disabled="ruleSaving || hasActivePendingStart" @change="saveRuleConfig">
                    </label>
                    <label>
                      <span>结果停留</span>
                      <input v-model.number="ruleTuning.roundResultSeconds" type="number" min="5" max="180" :disabled="ruleSaving || hasActivePendingStart" @change="saveRuleConfig">
                    </label>
                    <label>
                      <span>总榜停留</span>
                      <input v-model.number="ruleTuning.finalResultSeconds" type="number" min="5" max="180" :disabled="ruleSaving || hasActivePendingStart" @change="saveRuleConfig">
                    </label>
                  </template>
                  <template v-else>
                    <label>
                      <span>节奏</span>
                      <select v-model="ruleTuning.pace" :disabled="ruleSaving || hasActivePendingStart" @change="saveRuleConfig">
                        <option value="quick">快局 · 20 秒</option>
                        <option value="table">桌游局 · 30 秒</option>
                        <option value="late">慢聊局 · 45 秒</option>
                      </select>
                    </label>
                    <label>
                      <span>惩罚强度</span>
                      <select v-model="ruleTuning.stakes" :disabled="ruleSaving || hasActivePendingStart" @change="saveRuleConfig">
                        <option value="soft">轻罚</option>
                        <option value="classic">标准</option>
                        <option value="spicy">重罚</option>
                      </select>
                    </label>
                    <button class="permission-toggle rule-switch" type="button" :aria-pressed="ruleTuning.variant" :disabled="ruleSaving || hasActivePendingStart" @click="ruleTuning.variant = !ruleTuning.variant; saveRuleConfig()">
                      <span><component :is="ruleTuning.variant ? Check : X" :size="17" aria-hidden="true" /></span>
                      {{ ruleTuning.variant ? "扩展规则开启" : "基础规则" }}
                    </button>
                  </template>
                </section>

                <section v-if="viewerCanSeeRules || currentHost" class="rule-summary" aria-label="当前规则摘要">
                  <p class="eyebrow">完整摘要</p>
                  <ul>
                    <li v-for="item in ruleSummary" :key="item">{{ item }}</li>
                  </ul>
                  <small v-if="selectedDraft?.revision">规则版本 #{{ selectedDraft.revision }}</small>
                </section>
                <section v-else class="rule-redacted" aria-label="规则已隐藏">
                  <LockKeyhole :size="18" aria-hidden="true" />
                  <p>你还不是房间成员，规则细节已隐藏。</p>
                </section>

                <section v-if="currentHost" class="preset-panel" aria-label="房主规则预设">
                  <div class="preset-panel__save">
                    <input v-model="presetName" type="text" maxlength="32" placeholder="给这套规则命名" :disabled="presetsLoading" aria-label="规则预设名称">
                    <button class="mini-action" type="button" :disabled="!presetName.trim() || presetsLoading" @click="createRulePreset"><Save :size="17" aria-hidden="true" /></button>
                  </div>
                  <div class="preset-list" :aria-busy="presetsLoading">
                    <article v-for="preset in rulePresets" :key="preset.presetId" class="preset-item" :class="{ 'is-disabled': preset.compatible === false }">
                      <div><strong>{{ preset.name }}</strong><small>{{ preset.compatible === false ? "当前版本不可用" : "可应用到本局" }}</small></div>
                      <span>
                        <button class="mini-action" type="button" :disabled="preset.compatible === false || hasActivePendingStart" @click="applyRulePreset(preset)">用</button>
                        <button class="mini-action" type="button" :disabled="preset.compatible === false" @click="updateRulePreset(preset)">覆</button>
                        <button class="mini-action mini-action--danger" type="button" @click="deleteRulePreset(preset)"><Trash2 :size="16" aria-hidden="true" /></button>
                      </span>
                    </article>
                    <p v-if="!presetsLoading && rulePresets.length === 0" class="preset-empty">还没有规则预设。</p>
                  </div>
                </section>
              </div>
            </template>
          </ActionTray>
        </aside>
      </div>
    </section>

    <section class="member-roster panel" aria-labelledby="roster-title">
      <div class="member-roster__head">
        <div><p class="eyebrow">成员与座位</p><h2 id="roster-title" class="section-title">候场、观战与治理</h2></div>
        <p class="member-roster__meta">{{ participantMembers.length }} 人围桌，{{ waitingMembers.length }} 人候场，{{ spectatorMembers.length }} 人观战</p>
      </div>
      <div class="member-roster__list">
        <article v-for="member in displayMembers" :key="member.userId" class="member-row" :class="{ 'is-host': member.userId === displayHostUserId }">
          <span class="member-row__avatar">{{ displayMemberName(member.userId).slice(0, 1) }}</span>
          <div class="member-row__copy">
            <strong>{{ displayMemberName(member.userId) }}</strong>
            <small>{{ memberStatusLabel(member) }}</small>
          </div>
          <span class="member-row__actions">
            <Check v-if="member.role.includes('PARTICIPANT')" :size="18" aria-label="已入座" />
            <ChevronDown v-else-if="member.role.includes('WAITING')" :size="18" aria-label="候场中" />
            <Users v-else :size="18" aria-label="观战中" />
            <button
              v-if="member.role.includes('WAITING') && currentHost"
              class="mini-action"
              type="button"
              :aria-label="`晋升 ${displayMemberName(member.userId)}`"
              :disabled="governanceSaving"
              @click="approveMember(member.userId)"
            >
              <UserPlus :size="18" aria-hidden="true" />
            </button>
            <button
              v-if="currentHost && member.userId !== displayHostUserId"
              class="mini-action mini-action--danger"
              type="button"
              :aria-label="`移出 ${displayMemberName(member.userId)}`"
              :disabled="governanceSaving"
              @click="requestRemoveMember(member, $event)"
            >
              <UserMinus :size="18" aria-hidden="true" />
            </button>
          </span>
        </article>
      </div>
    </section>

    <section class="host-controls panel" aria-labelledby="host-title">
      <div><p class="eyebrow">房主管理</p><h2 id="host-title" class="section-title">开局许可与危险操作</h2></div>
      <div class="host-controls__body">
        <button class="permission-toggle" type="button" :aria-pressed="entryOpen" :disabled="isPlaying || (isRemote && !currentHost)" @click="toggleAdmission">
          <span><component :is="entryOpen ? Check : X" :size="17" aria-hidden="true" /></span>
          {{ entryOpen ? (isPostGame ? "开放下一局加入" : "本局开始前允许加入") : "新玩家进入等候区" }}
        </button>
        <label v-if="isPostGame && currentHost && replayAccess" class="replay-policy">
          <span>复盘可见范围</span>
          <select :value="replayAccess.policy" :disabled="replayAccessSaving" aria-label="复盘可见范围" @change="changeReplayPolicy">
            <option v-for="option in replayPolicyOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
        </label>
        <div v-if="currentHost" class="danger-zone">
          <p class="danger-zone__copy">解散后房间码立即失效；若对局正在进行，本局会终止并保留取消前已公开的进度。</p>
          <button class="danger-control" type="button" :disabled="governanceSaving" :title="isPlaying ? '取消当前对局并永久解散房间' : '永久解散这个房间'" @click="requestCloseRoom($event)">
            <DoorClosed :size="18" aria-hidden="true" /> 解散房间
          </button>
        </div>
      </div>
    </section>

    <DangerConfirm
      :open="governanceConfirmation !== null"
      :title="governanceTitle"
      :confirm-label="governanceSaving ? '正在处理' : governanceConfirmation?.kind === 'remove' ? '确认移出' : '确认解散'"
      @cancel="cancelGovernanceConfirmation"
      @confirm="confirmGovernance"
    >
      <div class="danger-dialog__copy">
        <p>{{ governanceDescription }}</p>
        <p v-if="actionError" class="danger-dialog__error" role="alert">{{ actionError }}</p>
      </div>
    </DangerConfirm>
    </template>
    <UsernameDialog ref="usernameDialog" @changed="handleUsernameChanged" />
  </main>
</template>

<style scoped>
.room-screen {
  --room-stage-min-height: clamp(480px, 68dvh, 640px);
  display: grid;
  align-content: start;
  gap: 20px;
}
.room-code { display: inline-flex; align-items: center; gap: 8px; }
.room-code > span { color: var(--platform-muted); font-size: 11px; }
.room-code strong { color: var(--platform-accent); font-size: 19px; letter-spacing: .08em; }
.room-code button { width: 34px; height: 34px; display: grid; place-items: center; color: var(--platform-muted); background: transparent; border: 0; }
.room-topbar__tools { min-width: 0; display: flex; align-items: center; justify-content: flex-end; gap: 9px; }
.room-count { display: inline-flex; align-items: center; gap: 5px; color: var(--platform-muted); font-size: 12px; }
.room-hero { padding: clamp(10px, 3vh, 28px) 0 0; }
.room-hero .display-title { max-width: 760px; }
.room-hero > .muted { max-width: 620px; line-height: 1.5; }
.room-hero__enter { width: fit-content; margin-top: 10px; }
.loading-note { color: var(--platform-muted); font-size: 13px; }
.form-error { margin: 0; color: var(--platform-danger); font-size: 13px; }
.profile-sync-notice { width: fit-content; margin: 10px 0 0; padding: 8px 10px; color: var(--platform-muted); border-left: 2px solid var(--platform-accent); font-size: 12px; }
.room-stage,
.member-roster,
.host-controls {
  display: grid;
  gap: 16px;
  padding: 18px;
  background: var(--room-surface-strong);
  border: 1px solid var(--room-border);
  box-shadow: var(--room-shadow);
}
.room-stage__head,
.member-roster__head { display: flex; align-items: end; justify-content: space-between; gap: 12px; }
.entry-status { display: inline-flex; align-items: center; gap: 6px; color: var(--room-success); font-size: 12px; }
.entry-status.is-closed { color: var(--platform-accent); }
.room-stage__table-shell { position: relative; display: grid; }
.room-stage__table {
  min-height: var(--room-stage-min-height);
  overflow: hidden;
  background: linear-gradient(180deg, var(--room-accent-surface), transparent 24%), var(--room-stage);
  border: 1px solid var(--room-border);
  border-radius: 8px;
}
.room-stage__rules { position: absolute; inset: auto 0 0 0; z-index: 20; pointer-events: none; }
.room-stage__rules :deep(.gn-tray) { pointer-events: auto; }
.room-stage__empty {
  min-height: var(--room-stage-min-height);
  display: grid;
  place-items: center;
  gap: 8px;
  color: var(--platform-muted);
}
.room-stage__empty p { margin: 0; }
.room-center-card {
  width: min(100%, 272px);
  display: grid;
  gap: 8px;
  padding: 12px;
  text-align: left;
  background: var(--room-surface);
  border: 1px solid var(--room-border);
  border-radius: 8px;
  box-shadow: var(--room-shadow);
}
.room-center-card > .eyebrow { margin-bottom: 0; font-size: 10px; }
.room-center-card__heading { min-width: 0; }
.room-center-card__heading > strong { font-size: 22px; line-height: 1.05; }
.room-center-card__rules {
  width: 48px;
  height: 48px;
  display: grid;
  place-items: center;
  color: var(--platform-ink);
  background: var(--room-surface-soft);
  border: 1px solid var(--room-border-strong);
  border-radius: 7px;
}
.room-center-card__summary {
  display: grid;
  gap: 3px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.room-center-card__summary li,
.room-center-card__summary.is-redacted {
  color: var(--platform-ink);
  font-size: 12px;
  line-height: 1.35;
}
.room-center-card__summary li::before { content: "•"; margin-right: 7px; color: var(--platform-accent); }
.room-center-card__actions { display: grid; grid-template-columns: 48px minmax(0, 1fr); gap: 8px; }
.room-center-card__start { width: 100%; }
.room-center-card__countdown { margin-top: 2px; }
.rule-tray__summary { min-height: 38px; display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.rule-tray__summary-copy { min-width: 0; display: grid; gap: 2px; }
.rule-tray__summary-copy strong { font-size: 15px; }
.rule-tray__summary-copy span,
.rule-tray__summary small { color: var(--platform-muted); font-size: 11px; line-height: 1.45; }
.rule-panel__body { display: grid; gap: 14px; }
.game-picker__head { display: flex; align-items: end; justify-content: space-between; gap: 12px; }
.game-picker__head > span { color: var(--platform-muted); font-size: 12px; }
.game-options { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.game-option {
  min-height: 128px;
  display: grid;
  grid-template-columns: 48px 1fr;
  align-content: start;
  gap: 7px 12px;
  padding: 16px;
  color: var(--platform-ink);
  text-align: left;
  background: var(--room-card);
  border: 1px solid var(--room-border);
  border-radius: 14px;
}
.game-option > span { grid-row: span 3; width: 48px; height: 48px; display: grid; place-items: center; color: var(--room-accent-ink); background: var(--platform-accent); border-radius: 50%; font-family: var(--font-display); font-weight: 900; }
.game-option strong { align-self: center; font-size: 17px; }
.game-option small { grid-column: 2; min-height: 40px; color: var(--platform-muted); line-height: 1.5; }
.game-option em { grid-column: 2; color: var(--platform-accent); font-size: 11px; font-style: normal; }
.game-option.is-selected { background: var(--room-accent-surface-strong); border-color: var(--room-border-strong); box-shadow: inset 0 0 0 1px var(--room-border-strong); }
.game-option:disabled { cursor: default; opacity: .72; }
.rule-controls { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.rule-controls label { display: grid; gap: 6px; color: var(--platform-muted); font-size: 12px; }
.rule-controls select,
.rule-controls input,
.preset-panel input,
.replay-policy select { min-height: 48px; width: 100%; padding: 0 12px; color: var(--platform-ink); background: var(--room-surface-soft); border: 1px solid var(--room-border); border-radius: 12px; }
.rule-controls select:disabled,
.rule-controls input:disabled,
.preset-panel input:disabled,
.replay-policy select:disabled { cursor: not-allowed; opacity: .6; }
.rule-switch { justify-content: start; align-self: end; }
.rule-summary { display: grid; gap: 8px; padding: 14px; background: var(--room-surface-soft); border: 1px solid var(--room-border); border-radius: 14px; }
.rule-summary--tray { min-height: 100%; }
.rule-summary ul { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.rule-summary li { color: var(--platform-ink); font-size: 13px; line-height: 1.45; }
.rule-summary li::before { content: "•"; margin-right: 7px; color: var(--platform-accent); }
.rule-summary small { color: var(--platform-muted); font-size: 11px; }
.rule-redacted { min-height: 82px; display: flex; align-items: center; gap: 10px; padding: 14px; color: var(--platform-muted); background: var(--room-surface-soft); border: 1px dashed var(--room-border-dashed); border-radius: 14px; }
.rule-redacted p { margin: 0; font-size: 13px; }
.preset-panel { display: grid; gap: 10px; }
.preset-panel__save { display: grid; grid-template-columns: minmax(0, 1fr) 48px; gap: 8px; }
.preset-list { display: grid; gap: 8px; }
.preset-item { min-height: 58px; display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 10px; padding: 12px; background: var(--room-card); border: 1px solid var(--room-border); border-radius: 14px; }
.preset-item.is-disabled { opacity: .58; }
.preset-item div { min-width: 0; display: grid; gap: 2px; }
.preset-item strong,
.preset-item small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preset-item small,
.preset-empty { color: var(--platform-muted); font-size: 11px; }
.preset-item span { display: inline-flex; gap: 6px; }
.preset-item .mini-action { width: 44px; height: 44px; font-size: 12px; font-weight: 800; }
.preset-empty { margin: 0; }
.countdown-card { display: grid; grid-template-columns: 36px minmax(0, 1fr) auto; align-items: center; gap: 10px; padding: 12px; color: var(--platform-ink); background: var(--room-accent-surface); border: 1px solid var(--room-border-strong); border-radius: 14px; }
.countdown-card > svg { color: var(--platform-accent); }
.countdown-card div { display: grid; gap: 2px; }
.countdown-card strong { color: var(--platform-accent); font-size: 28px; line-height: 1; }
.countdown-card span { color: var(--platform-muted); font-size: 12px; }
.member-roster__meta,
.danger-zone__copy { margin: 0; color: var(--platform-muted); font-size: 12px; line-height: 1.55; }
.member-roster__list { display: grid; gap: 10px; }
.member-row {
  min-height: 72px;
  display: grid;
  grid-template-columns: 48px minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  background: var(--room-card);
  border: 1px solid var(--room-border);
  border-radius: 14px;
}
.member-row.is-host { border-color: var(--room-border-strong); }
.member-row__avatar {
  width: 48px;
  height: 48px;
  display: grid;
  place-items: center;
  color: var(--room-accent-ink);
  background: var(--platform-accent);
  border-radius: 50%;
  font-weight: 800;
}
.member-row__copy { min-width: 0; display: grid; gap: 3px; }
.member-row__copy strong,
.member-row__copy small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.member-row__copy small { color: var(--platform-muted); font-size: 12px; }
.member-row__actions { display: inline-flex; flex-wrap: wrap; align-items: center; justify-content: end; gap: 8px; }
.member-row__actions > svg { color: var(--room-success); }
.host-controls__body { display: grid; gap: 12px; }
.permission-toggle {
  min-height: 48px;
  display: inline-flex;
  align-items: center;
  gap: 9px;
  padding: 0 14px;
  color: var(--platform-ink);
  background: var(--room-surface-soft);
  border: 1px solid var(--room-border);
  border-radius: 12px;
}
.permission-toggle > span {
  width: 28px;
  height: 28px;
  display: grid;
  place-items: center;
  color: var(--room-accent-ink);
  background: var(--room-success);
  border-radius: 7px;
}
.permission-toggle:disabled { cursor: not-allowed; opacity: .55; }
.replay-policy { display: grid; gap: 6px; color: var(--platform-muted); font-size: 12px; }
.danger-zone { display: grid; gap: 8px; padding: 14px; background: var(--room-danger-surface); border: 1px solid var(--room-border-danger); border-radius: 14px; }
.mini-action {
  width: 48px;
  height: 48px;
  display: grid;
  place-items: center;
  color: var(--platform-accent);
  background: var(--room-surface-soft);
  border: 1px solid var(--room-border-strong);
  border-radius: 12px;
}
.mini-action--danger { color: var(--platform-danger); border-color: var(--room-border-danger); }
.mini-action:disabled { cursor: not-allowed; opacity: .5; }
.danger-control {
  min-height: 48px;
  width: fit-content;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 0 14px;
  color: var(--platform-danger);
  background: transparent;
  border: 1px solid var(--room-border-danger);
  border-radius: 12px;
}
.danger-control:disabled { cursor: not-allowed; color: var(--platform-muted); border-color: var(--room-border); opacity: .6; }
.danger-dialog__copy { display: grid; gap: 8px; }
.danger-dialog__copy p { margin: 0; }
.danger-dialog__error { color: var(--platform-danger); }

@media (orientation: landscape) and (min-width: 768px) {
  .room-stage__table-shell { grid-template-columns: minmax(0, 1fr) clamp(260px, 30vw, 360px); gap: 18px; align-items: stretch; }
  .room-stage__table { min-height: clamp(360px, 72vh, 580px); }
  .room-stage__rules { position: relative; inset: auto; min-height: 100%; pointer-events: auto; }
  .room-stage__rules :deep(.gn-tray) {
    position: sticky;
    top: 0;
    inset-inline: auto;
    bottom: auto;
    height: 100%;
    min-height: 100%;
    max-height: none;
    padding: 16px;
    background: var(--room-surface-strong);
    border: 1px solid var(--room-border);
    border-radius: 18px;
    box-shadow: var(--room-shadow-strong);
  }
  .room-stage__rules :deep(.gn-tray.is-collapsed),
  .room-stage__rules :deep(.gn-tray.is-compact),
  .room-stage__rules :deep(.gn-tray.is-expanded) { --tray-height: auto; }
  .room-stage__rules :deep(.gn-tray__handle) {
    top: 12px;
    left: auto;
    right: 12px;
    width: 48px;
    height: 48px;
    transform: none;
    background: var(--room-surface);
    border-color: var(--room-border);
    border-radius: 999px;
  }
  .room-stage__rules :deep(.gn-tray__content) { padding-top: 34px; }
  .room-stage__rules :deep(.gn-tray__summary),
  .room-stage__rules :deep(.gn-tray__primary),
  .room-stage__rules :deep(.gn-tray__details) { width: 100%; }
  .room-center-card { width: min(100%, 292px); }
  .game-options,
  .rule-controls { grid-template-columns: 1fr; }
}

@media (max-width: 720px) {
  .room-screen { --room-stage-min-height: clamp(520px, 70dvh, 600px); gap: 14px; }
  .room-screen > .topbar { gap: 7px; }
  .room-code { gap: 4px; }
  .room-code > span { display: none; }
  .room-code strong { font-size: 17px; }
  .room-topbar__tools { gap: 6px; }
  .room-hero { padding-top: 0; }
  .room-hero > .eyebrow { margin-bottom: 4px; }
  .room-hero .display-title { font-size: 32px; line-height: 1; }
  .room-hero > .muted { margin: 9px 0 0; font-size: 13px; line-height: 1.45; }
  .room-stage,
  .member-roster,
  .host-controls { padding: 12px; }
  .room-stage { gap: 10px; }
  .room-stage__head .eyebrow { margin-bottom: 4px; }
  .room-stage__head .section-title { font-size: 22px; }
  .room-stage__table { min-height: var(--room-stage-min-height); }
  .room-stage__table :deep(.gn-table__center) { transform: translateY(calc(18px - var(--gn-safe-center-shift, 0px))); }
  .room-center-card { width: min(100%, 224px); gap: 6px; padding: 10px; }
  .room-center-card > .eyebrow { display: none; }
  .room-center-card__heading > strong { font-size: 20px; }
  .room-center-card__summary li { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .room-center-card.is-tray-compact > .room-center-card__heading,
  .room-center-card.is-tray-compact > .room-center-card__summary { display: none; }
  .room-center-card.is-tray-expanded { display: none; }
  .game-options,
  .rule-controls { grid-template-columns: 1fr; }
  .countdown-card { grid-template-columns: 32px minmax(0, 1fr); }
  .countdown-card .danger-control { grid-column: 1 / -1; width: 100%; justify-content: center; }
  .member-row { grid-template-columns: 48px minmax(0, 1fr); align-items: start; }
  .member-row__actions { grid-column: 1 / -1; justify-content: start; }
}

@media (orientation: landscape) and (min-width: 768px) and (max-height: 520px) {
  .room-screen { gap: 8px; }
  .room-hero { display: none; }
  .room-stage { gap: 8px; padding: 10px; }
  .room-stage__head { align-items: center; }
  .room-stage__head .eyebrow { display: none; }
  .room-stage__head .section-title { font-size: 18px; }
  .room-stage__table-shell { grid-template-columns: minmax(0, 1fr) 260px; gap: 10px; }
  .room-stage__table { min-height: calc(100dvh - 124px); }
  .room-center-card { width: min(100%, 224px); gap: 6px; padding: 10px; }
  .room-center-card > .eyebrow,
  .room-center-card__summary { display: none; }
  .room-center-card__heading > strong { font-size: 20px; }
  .room-stage__rules :deep(.gn-tray__handle) { width: 48px; height: 48px; }
}
</style>
