import { defineStore } from "pinia";

import { ApiError, identityClient, isDevelopmentFallbackAllowed, roomClient, type GameEnvelopeInput, type RoomSnapshot } from "../api/client";

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
  }),
  getters: {
    hasIdentity: (state) => state.displayName.length > 0 && state.userId.length > 0 && state.identityState !== "anonymous",
    hasActiveRoom: (state) => state.roomId !== null,
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

    /**
     * Restores the server-owned device identity. A local context is retained
     * only as a development fallback so fixture pages stay usable without API.
     */
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
        if (isDevelopmentFallbackAllowed() && (error instanceof ApiError ? error.status === 401 || error.status === 403 || error.status === 404 : true)) {
          this.identityState = this.displayName.length > 0 ? "active" : "anonymous";
          return;
        }
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
        if (isDevelopmentFallbackAllowed() && error instanceof ApiError && (error.status === 401 || error.status === 403 || error.status === 404)) {
          if (!this.setIdentity(normalized)) {
            throw error;
          }
          return;
        }
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
        this.remoteRoom = response.room;
        this.roomId = response.room.roomId;
        this.roomCode = response.room.roomCode;
        this.persist();
        return response.room;
      } catch (error) {
        if (isDevelopmentFallbackAllowed() && error instanceof ApiError && (error.status === 401 || error.status === 403 || error.status === 404)) {
          return null;
        }
        this.error = error instanceof Error ? error.message : "房间加载失败";
        throw error;
      } finally {
        this.busy = false;
      }
    },

    /** Joins or queues this device and replaces the locally displayed snapshot. */
    async joinRemote(roomCode: string, intent: "JOIN_INTENT_PARTICIPANT" | "JOIN_INTENT_SPECTATOR" = "JOIN_INTENT_PARTICIPANT"): Promise<RoomSnapshot | null> {
      try {
        const normalizedCode = roomCode.trim().toUpperCase();
        // A CAS token is room-specific; carrying the previous room's version into a new invite causes a false conflict.
        const knownVersion = this.remoteRoom?.roomCode === normalizedCode ? this.remoteRoom.version : undefined;
        const response = await roomClient.joinRoom(normalizedCode, intent, knownVersion);
        if (response.room) {
          this.remoteRoom = response.room;
          this.roomId = response.room.roomId;
          this.roomCode = response.room.roomCode;
          this.persist();
        }
        return response.room ?? null;
      } catch (error) {
        if (isDevelopmentFallbackAllowed() && error instanceof ApiError && (error.status === 401 || error.status === 403 || error.status === 404)) {
          return null;
        }
        throw error;
      }
    },

    /** Creates a room through the host command and stores its server-issued code. */
    async createRemoteRoom(): Promise<RoomSnapshot | null> {
      try {
        const response = await roomClient.createRoom();
        if (response.room) {
          this.setRemoteRoom(response.room);
        }
        return response.room ?? null;
      } catch (error) {
        if (isDevelopmentFallbackAllowed() && error instanceof ApiError && (error.status === 401 || error.status === 403 || error.status === 404)) {
          return null;
        }
        throw error;
      }
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

    /** Starts a new child session without losing the continuous room context. */
    async startRemoteGame(gameId = "liars-dice"): Promise<RoomResponseLike> {
      if (!this.remoteRoom) {
        return { sessionId: "" };
      }
      const response = await roomClient.startGame(this.remoteRoom, this.userId, gameId);
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
      this.remoteRoom = snapshot;
      this.roomId = snapshot.roomId;
      this.roomCode = snapshot.roomCode;
      this.persist();
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
