import { defineStore } from "pinia";

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
  state: () => blankContext(),
  getters: {
    hasIdentity: (state) => state.displayName.length > 0 && state.userId.length > 0,
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
      this.persist();
      return true;
    },

    enterRoom(roomId: string, roomCode = roomId.toUpperCase().slice(0, 6)): void {
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

export { STORAGE_KEY };
