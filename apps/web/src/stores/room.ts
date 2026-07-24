import { defineStore } from "pinia";

import {
  ApiError,
  identityClient,
  roomClient,
  type GameEnvelopeInput,
  type GameEnvelopeWire,
  type GameRulePresetWire,
  type GameRulePresetWriteMode,
  type MyRoomCardWire,
  type PendingGameStartWire,
  type PublicRoomCardWire,
  type RoomGameConfigDraftWire,
  type RoomSnapshot,
} from "../api/client";

const STORAGE_KEY = "game-night.room-context.v1";
const STORAGE_SCHEMA_VERSION = 1;

interface PersistedRoomContext {
  schemaVersion: number;
  displayName: string;
  userId: string;
  roomId: string | null;
  roomCode: string | null;
  sessionId: string | null;
}

type IdentityState = "unknown" | "anonymous" | "onboarding" | "active";

const blankContext = (): PersistedRoomContext => ({
  schemaVersion: STORAGE_SCHEMA_VERSION,
  displayName: "",
  userId: "",
  roomId: null,
  roomCode: null,
  sessionId: null,
});

const asOptionalString = (value: unknown): string | null => (typeof value === "string" && value.length > 0 ? value : null);

export const useRoomStore = defineStore("room", {
  state: () => ({
    ...blankContext(),
    identityState: "unknown" as IdentityState,
    remoteRoom: null as RoomSnapshot | null,
    busy: false,
    error: "",
    notice: "",
    myRooms: [] as MyRoomCardWire[],
    publicRooms: [] as PublicRoomCardWire[],
    myRoomsNextPageToken: "",
    publicRoomsNextPageToken: "",
    myRoomsLoading: false,
    publicRoomsLoading: false,
    selectedGameId: "",
    gameConfigDrafts: [] as RoomGameConfigDraftWire[],
    pendingStart: null as PendingGameStartWire | null,
    ownershipEpoch: "",
    gameRulePresets: [] as GameRulePresetWire[],
    gameRulePresetsLoading: false,
  }),
  getters: {
    hasIdentity: (state) => state.displayName.length > 0 && state.userId.length > 0 && state.identityState !== "anonymous",
    hasActiveRoom: (state) => state.roomId !== null,
    currentGameId: (state) => state.selectedGameId || state.remoteRoom?.activeGameId || "liars-dice",
    currentGameConfigDraft: (state) => state.gameConfigDrafts.find((draft) => draft.gameId === (state.selectedGameId || state.remoteRoom?.activeGameId)),
  },
  actions: {
    recover(): void {
      if (typeof window === "undefined") {
        return;
      }
      try {
        const raw = window.localStorage.getItem(STORAGE_KEY);
        if (raw === null) {
          return;
        }
        const parsed: unknown = JSON.parse(raw);
        if (typeof parsed !== "object" || parsed === null || (parsed as { schemaVersion?: unknown }).schemaVersion !== STORAGE_SCHEMA_VERSION) {
          return;
        }
        const candidate = parsed as Partial<PersistedRoomContext>;
        if (typeof candidate.displayName !== "string" || typeof candidate.userId !== "string") {
          return;
        }
        this.$patch({
          displayName: candidate.displayName.slice(0, 18),
          userId: candidate.userId.slice(0, 80),
          roomId: asOptionalString(candidate.roomId),
          roomCode: asOptionalString(candidate.roomCode),
          sessionId: asOptionalString(candidate.sessionId),
        });
        this.identityState = this.displayName.length > 0 ? "active" : "anonymous";
      } catch {
        // A corrupt local context must never prevent the shell from opening.
        window.localStorage.removeItem(STORAGE_KEY);
      }
    },

    setIdentity(displayName: string): boolean {
      const normalized = displayName.trim().replace(/\s+/g, " ");
      if (normalized.length < 1 || normalized.length > 18) {
        return false;
      }
      // This id is only a local correlation key; device secrets stay outside persisted UI context.
      this.displayName = normalized;
      this.userId = this.userId || `guest-${crypto.randomUUID()}`;
      this.identityState = "active";
      this.persist();
      return true;
    },

    /** Restores the server-owned device identity without trusting local room context as authentication. */
    async recoverIdentity(): Promise<void> {
      try {
        const response = await identityClient.current();
        const user = response.user;
        if (!user?.userId) {
          this.identityState = "anonymous";
          return;
        }
        this.userId = user.userId;
        this.displayName = user.username ?? "";
        this.identityState = normalizeIdentityState(user.status, this.displayName);
        this.persist();
      } catch (error) {
        if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
          this.identityState = "anonymous";
          return;
        }
        throw error;
      }
    },

    /**
     * Completes onboarding through the device challenge, then updates the
     * local recovery context with the server's immutable user ID.
     */
    async ensureIdentity(displayName: string): Promise<void> {
      const normalized = normalizeDisplayName(displayName);
      if (normalized.length < 1 || normalized.length > 18) {
        throw new Error("用户名需要 1 到 18 个字符");
      }
      this.busy = true;
      this.error = "";
      try {
        await this.recoverIdentity();
        if (this.identityState === "active" && this.displayName.length > 0) {
          return;
        }
        if (String(this.identityState) === "onboarding") {
          const onboarded = await identityClient.completeOnboarding(normalized);
          this.applyIdentity(onboarded.user, normalized);
          return;
        }
        const requestFlowId = requestID();
        const begun = await identityClient.beginBootstrap(requestFlowId);
        const proof = begun.challenge?.challengeProof;
        if (!proof) {
          throw new Error("设备身份挑战无效");
        }
        const bootstrapped = await identityClient.bootstrap(proof, requestID(), requestFlowId);
        this.applyIdentity(bootstrapped.user, "");
        if (String(this.identityState) === "onboarding") {
          const onboarded = await identityClient.completeOnboarding(normalized);
          this.applyIdentity(onboarded.user, normalized);
        }
      } catch (error) {
        this.error = error instanceof Error ? error.message : "身份初始化失败";
        throw error;
      } finally {
        this.busy = false;
      }
    },

    /** Loads an authoritative room snapshot addressed by ID or invitation code. */
    async loadRoom(roomId?: string, roomCode?: string): Promise<RoomSnapshot | null> {
      this.busy = true;
      this.error = "";
      try {
        const response = await roomClient.getRoom(roomId, roomCode);
        if (!response.room) {
          throw new Error("房间响应缺少状态");
        }
        this.setRemoteRoom(response.room);
        return response.room;
      } catch (error) {
        this.error = error instanceof Error ? error.message : "房间加载失败";
        throw error;
      } finally {
        this.busy = false;
      }
    },

    /** Joins or queues this device and replaces the locally displayed snapshot. */
    async joinRemote(roomCode: string, intent: "JOIN_INTENT_PARTICIPANT" | "JOIN_INTENT_SPECTATOR" = "JOIN_INTENT_PARTICIPANT"): Promise<RoomSnapshot> {
      const normalizedCode = roomCode.trim().toUpperCase();
      // A CAS token is room-specific; carrying the previous room's version into a new invite causes a false conflict.
      const knownVersion = this.remoteRoom?.roomCode === normalizedCode ? this.remoteRoom.version : undefined;
      const response = await roomClient.joinRoom(normalizedCode, intent, knownVersion);
      if (!response.room) {
        throw new Error("加入房间响应缺少状态");
      }
      this.setRemoteRoom(response.room);
      return response.room;
    },

    /** Creates a room through the host command and stores its server-issued code. */
    async createRemoteRoom(visibility: "ROOM_VISIBILITY_PRIVATE" | "ROOM_VISIBILITY_PUBLIC" = "ROOM_VISIBILITY_PRIVATE"): Promise<RoomSnapshot> {
      const response = await roomClient.createRoom(visibility);
      if (!response.room) {
        throw new Error("创建房间响应缺少状态");
      }
      this.setRemoteRoom(response.room);
      return response.room;
    },

    /** Loads the actor's authoritative active rooms; reset is used after create, close, or identity recovery. */
    async loadMyRooms(reset = true): Promise<void> {
      if (this.myRoomsLoading) return;
      this.myRoomsLoading = true;
      try {
        const pageToken = reset ? "" : this.myRoomsNextPageToken;
        if (!reset && pageToken === "") return;
        const response = await roomClient.listMyRooms(pageToken);
        const rooms = response.rooms ?? [];
        this.myRooms = reset ? [...rooms] : [...this.myRooms, ...rooms];
        this.myRoomsNextPageToken = response.page?.nextPageToken ?? "";
      } finally {
        this.myRoomsLoading = false;
      }
    },

    /** Loads discoverable public rooms independently so one failed list never hides the actor's own rooms. */
    async loadPublicRooms(reset = true): Promise<void> {
      if (this.publicRoomsLoading) return;
      this.publicRoomsLoading = true;
      try {
        const pageToken = reset ? "" : this.publicRoomsNextPageToken;
        if (!reset && pageToken === "") return;
        const response = await roomClient.listPublicRooms(pageToken);
        const rooms = response.rooms ?? [];
        this.publicRooms = reset ? [...rooms] : [...this.publicRooms, ...rooms];
        this.publicRoomsNextPageToken = response.page?.nextPageToken ?? "";
      } finally {
        this.publicRoomsLoading = false;
      }
    },

    /** Joins a discoverable room by public ID; private invites continue to use room codes. */
    async joinPublicRemote(roomId: string, intent: "JOIN_INTENT_PARTICIPANT" | "JOIN_INTENT_SPECTATOR"): Promise<RoomSnapshot> {
      const response = await roomClient.joinPublicRoom(roomId, intent);
      if (!response.room) throw new Error("加入公开房间响应缺少状态");
      this.setRemoteRoom(response.room);
      return response.room;
    },

    /** Applies host admission policy with the snapshot version as a CAS token. */
    async setAdmissionRemote(participantAdmission: string, spectatorAdmission: string): Promise<RoomSnapshot | null> {
      if (!this.remoteRoom) {
        return null;
      }
      const response = await roomClient.setAdmission(this.remoteRoom, participantAdmission, spectatorAdmission);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      return response.room ?? null;
    },

    /** Host-selects the pregame rules table and adopts the server's selected_game_id snapshot. */
    async selectRemoteGame(gameId: string): Promise<RoomSnapshot> {
      if (!this.remoteRoom) {
        throw new Error("尚未进入房间");
      }
      const response = await roomClient.selectRoomGame(this.remoteRoom, gameId);
      if (!response.room) throw new Error("游戏选择响应缺少房间状态");
      this.setRemoteRoom(response.room);
      return response.room;
    },

    /** Legacy alias kept for RoomView compatibility while the UI migrates to the explicit remote action names. */
    async selectGame(gameId: string): Promise<RoomSnapshot> {
      return this.selectRemoteGame(gameId);
    },

    /** Persists one complete server-normalized config draft under the current room ownership epoch. */
    async updateRemoteGameConfig(gameId: string, config: GameEnvelopeInput | GameEnvelopeWire | undefined, expectedRevision?: string | number | bigint): Promise<RoomGameConfigDraftWire | null> {
      if (!this.remoteRoom) {
        return null;
      }
      if (!config) {
        return null;
      }
      const revision = expectedRevision ?? this.gameConfigDrafts.find((draft) => draft.gameId === gameId)?.revision ?? "0";
      const response = await roomClient.updateGameConfig(this.remoteRoom, gameId, config, revision);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      if (response.draft) {
        this.upsertGameConfigDraft(response.draft);
      }
      return response.draft ?? null;
    },

    /** Legacy alias kept for RoomView compatibility while the UI migrates to the explicit remote action names. */
    async updateGameConfig(gameId: string, config: GameEnvelopeInput | GameEnvelopeWire | undefined, expectedRevision?: string | number | bigint): Promise<RoomGameConfigDraftWire | null> {
      return this.updateRemoteGameConfig(gameId, config, expectedRevision);
    },

    /** Loads this user's personal reusable presets for the selected game. */
    async loadGameRulePresets(gameId?: string): Promise<GameRulePresetWire[]> {
      const targetGameId = gameId ?? this.currentGameId;
      if (this.gameRulePresetsLoading) {
        return this.gameRulePresets;
      }
      this.gameRulePresetsLoading = true;
      try {
        const response = await roomClient.listGameRulePresets(targetGameId);
        this.gameRulePresets = response.presets ?? [];
        return this.gameRulePresets;
      } finally {
        this.gameRulePresetsLoading = false;
      }
    },

    /** Legacy alias kept for RoomView compatibility while the UI migrates to the explicit remote action names. */
    async listGameRulePresets(gameId?: string): Promise<GameRulePresetWire[]> {
      return this.loadGameRulePresets(gameId);
    },

    /** Legacy alias that maps the old create/update preset surface onto the server's save RPC. */
    async createGameRulePreset(gameId: string, name: string, config: GameEnvelopeInput): Promise<GameRulePresetWire | null> {
      return this.saveGameRulePreset({
        gameId,
        name,
        config,
        mode: "GAME_RULE_PRESET_WRITE_MODE_CREATE",
        expectedPresetRevision: "0",
      });
    },

    /** Legacy alias that maps the old overwrite preset surface onto the server's save RPC. */
    async updateGameRulePreset(preset: GameRulePresetWire, config: GameEnvelopeInput, name = preset.name): Promise<GameRulePresetWire | null> {
      return this.saveGameRulePreset({
        presetId: preset.presetId,
        gameId: preset.gameId,
        name,
        config,
        mode: "GAME_RULE_PRESET_WRITE_MODE_OVERWRITE",
        expectedPresetRevision: preset.presetRevision ?? "0",
      });
    },

    /** Creates, overwrites, or copies a personal preset using the backend's optimistic preset revision. */
    async saveGameRulePreset(input: {
      presetId?: string;
      gameId?: string;
      name: string;
      config: GameEnvelopeInput | GameEnvelopeWire;
      mode: GameRulePresetWriteMode;
      expectedPresetRevision?: string | number | bigint;
    }): Promise<GameRulePresetWire | null> {
      const response = await roomClient.saveGameRulePreset({
        presetId: input.presetId,
        gameId: input.gameId ?? this.currentGameId,
        name: input.name,
        config: input.config,
        mode: input.mode,
        expectedPresetRevision: input.expectedPresetRevision,
      });
      if (response.preset) {
        this.upsertGameRulePreset(response.preset);
      }
      return response.preset ?? null;
    },

    /** Deletes one personal preset and removes it from the local preset list only after the server confirms. */
    async deleteGameRulePreset(presetOrId: string | GameRulePresetWire, expectedPresetRevision?: string | number | bigint): Promise<string> {
      const presetId = typeof presetOrId === "string" ? presetOrId : presetOrId.presetId;
      const revision = expectedPresetRevision ?? (typeof presetOrId === "string" ? "0" : presetOrId.presetRevision ?? "0");
      const response = await roomClient.deleteGameRulePreset(presetId, revision);
      const deletedId = response.presetId || presetId;
      this.gameRulePresets = this.gameRulePresets.filter((preset) => preset.presetId !== deletedId);
      return deletedId;
    },

    /** Legacy alias that keeps the RoomView delete flow compiling during the migration. */
    async deleteGameRulePresetById(presetOrId: string | GameRulePresetWire, expectedPresetRevision?: string | number | bigint): Promise<string> {
      return this.deleteGameRulePreset(presetOrId, expectedPresetRevision);
    },

    /** Creates the authoritative countdown and synchronizes pending_start from the server response. */
    async beginRemoteGameStart(gameId?: string, configRevision?: string | number | bigint): Promise<PendingGameStartWire | null> {
      const targetGameId = gameId ?? this.currentGameId;
      if (!this.remoteRoom) {
        return null;
      }
      const revision = configRevision ?? this.gameConfigDrafts.find((draft) => draft.gameId === targetGameId)?.revision ?? "0";
      const response = await roomClient.beginGameStart(this.remoteRoom, targetGameId, revision);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      if (response.pendingStart) {
        this.pendingStart = response.pendingStart;
        this.remoteRoom = this.remoteRoom ? { ...this.remoteRoom, pendingStart: response.pendingStart } : this.remoteRoom;
      }
      return response.pendingStart ?? this.pendingStart;
    },

    /** Legacy alias kept for RoomView compatibility while the UI migrates to the explicit remote action names. */
    async beginGameStart(gameId?: string, configRevision?: string | number | bigint): Promise<PendingGameStartWire | null> {
      return this.beginRemoteGameStart(gameId, configRevision);
    },

    /** Cancels the server countdown using the opaque token from the latest room snapshot. */
    async cancelRemoteGameStart(pendingStart?: PendingGameStartWire | null): Promise<RoomSnapshot | null> {
      const targetPendingStart = pendingStart ?? this.pendingStart;
      if (!this.remoteRoom || !targetPendingStart) {
        return null;
      }
      const response = await roomClient.cancelGameStart(this.remoteRoom, targetPendingStart);
      if (response.room) {
        this.setRemoteRoom(response.room);
      } else {
        this.pendingStart = null;
      }
      return response.room ?? null;
    },

    /** Legacy alias kept for RoomView compatibility while the UI migrates to the explicit remote action names. */
    async cancelGameStart(pendingStart?: PendingGameStartWire | null): Promise<RoomSnapshot | null> {
      return this.cancelRemoteGameStart(pendingStart);
    },

    /** Starts a new child session without losing the continuous room context. */
    async startRemoteGame(gameId?: string): Promise<RoomResponseLike> {
      const targetGameId = gameId ?? this.currentGameId;
      if (!this.remoteRoom) {
        return { sessionId: "" };
      }
      const response = await roomClient.startGame(this.remoteRoom, this.userId, targetGameId);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      if (response.sessionId) {
        this.setSession(response.sessionId);
      }
      return response;
    },

    /** Finishes the active session and applies the atomically returned post-game room. */
    async finishRemoteGame(sessionId: string, expectedStateVersion: number, command: GameEnvelopeInput): Promise<RoomSnapshot | null> {
      if (!this.remoteRoom) {
        return null;
      }
      const response = await roomClient.finishGame(this.remoteRoom, this.userId, sessionId, expectedStateVersion, command);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      return response.room ?? null;
    },

    /** Promotes one waiting member after the host's current membership version is checked. */
    async approveRemoteMember(userId: string): Promise<RoomSnapshot | null> {
      if (!this.remoteRoom) {
        return null;
      }
      const response = await roomClient.approveMember(this.remoteRoom, userId);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      return response.room ?? null;
    },

    /** Removes one member and adopts the server's membership version before any later host command. */
    async removeRemoteMember(userId: string): Promise<RoomSnapshot | null> {
      if (!this.remoteRoom) {
        return null;
      }
      const response = await roomClient.removeMember(this.remoteRoom, userId);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      return response.room ?? null;
    },

    /** Closes the current idle room while retaining the terminal snapshot until the view exits. */
    async closeRemoteRoom(): Promise<RoomSnapshot | null> {
      if (!this.remoteRoom) {
        return null;
      }
      const response = await roomClient.closeRoom(this.remoteRoom);
      if (response.room) {
        this.setRemoteRoom(response.room);
      }
      return response.room ?? null;
    },

    setRemoteRoom(snapshot: RoomSnapshot): void {
      const normalized = normalizeRoomSnapshot(snapshot);
      this.remoteRoom = normalized;
      this.roomId = normalized.roomId;
      this.roomCode = normalized.roomCode;
      this.syncRuleState(normalized);
      this.persist();
    },

    /** Keeps host-rule state derived from the authoritative Room snapshot instead of stale UI-only fields. */
    syncRuleState(snapshot: RoomSnapshot): void {
      this.selectedGameId = snapshot.selectedGameId || snapshot.activeGameId || this.selectedGameId || "liars-dice";
      this.gameConfigDrafts = [...(snapshot.gameConfigDrafts ?? [])];
      this.pendingStart = snapshot.pendingStart ?? null;
      this.ownershipEpoch = String(snapshot.ownershipEpoch ?? "");
    },

    upsertGameConfigDraft(draft: RoomGameConfigDraftWire): void {
      const next = this.gameConfigDrafts.filter((candidate) => candidate.gameId !== draft.gameId);
      this.gameConfigDrafts = [...next, draft];
      this.remoteRoom = this.remoteRoom ? { ...this.remoteRoom, gameConfigDrafts: this.gameConfigDrafts } : this.remoteRoom;
    },

    upsertGameRulePreset(preset: GameRulePresetWire): void {
      const next = this.gameRulePresets.filter((candidate) => candidate.presetId !== preset.presetId);
      this.gameRulePresets = [...next, preset];
    },

    enterRoom(roomId: string, roomCode = roomId.toUpperCase().slice(0, 6)): void {
      if (this.remoteRoom && this.remoteRoom.roomId !== roomId.trim()) {
        this.remoteRoom = null;
      }
      this.roomId = roomId.trim();
      this.roomCode = roomCode.trim().toUpperCase();
      this.sessionId = null;
      this.persist();
    },

    setSession(sessionId: string): void {
      this.sessionId = sessionId;
      this.persist();
    },

    leaveRoom(): void {
      this.roomId = null;
      this.roomCode = null;
      this.sessionId = null;
      this.remoteRoom = null;
      this.selectedGameId = "";
      this.gameConfigDrafts = [];
      this.pendingStart = null;
      this.ownershipEpoch = "";
      this.persist();
    },

    /** Clears room recovery data while keeping one user-facing reason available on the destination page. */
    exitRoom(message: string): void {
      this.notice = message.trim();
      this.leaveRoom();
    },

    clearNotice(): void {
      this.notice = "";
    },

    applyIdentity(user: { userId?: string; username?: string; status?: string } | undefined, fallbackName: string): void {
      if (user?.userId) {
        this.userId = user.userId;
      }
      this.displayName = user?.username || fallbackName;
      this.identityState = normalizeIdentityState(user?.status ?? "", this.displayName);
      this.persist();
    },

    persist(): void {
      if (typeof window === "undefined") {
        return;
      }
      const snapshot: PersistedRoomContext = {
        schemaVersion: STORAGE_SCHEMA_VERSION,
        displayName: this.displayName,
        userId: this.userId,
        roomId: this.roomId,
        roomCode: this.roomCode,
        sessionId: this.sessionId,
      };
      try {
        window.localStorage.setItem(STORAGE_KEY, JSON.stringify(snapshot));
      } catch {
        // Storage is an optional recovery aid; an unavailable quota must not break gameplay.
      }
    },
  },
});

type RoomResponseLike = { sessionId?: string };

const normalizeDisplayName = (value: string): string => value.trim().replace(/\s+/g, " ");

const normalizeRoomSnapshot = (snapshot: RoomSnapshot): RoomSnapshot => ({
  ...snapshot,
  selectedGameId: snapshot.selectedGameId || snapshot.activeGameId || "liars-dice",
  gameConfigDrafts: [...(snapshot.gameConfigDrafts ?? [])],
  ...(snapshot.pendingStart === undefined ? {} : { pendingStart: snapshot.pendingStart }),
  ownershipEpoch: String(snapshot.ownershipEpoch ?? ""),
});

const normalizeIdentityState = (status: string, username: string): IdentityState => {
  if (status.includes("ONBOARDING") || status === "onboarding") {
    return "onboarding";
  }
  if (status.includes("ACTIVE") || status === "active" || username.length > 0) {
    return "active";
  }
  return "anonymous";
};

const requestID = (): string => {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `web-${Date.now()}-${Math.random().toString(16).slice(2)}`;
};

export { STORAGE_KEY };
