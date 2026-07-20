import { describe, expect, it, vi } from "vitest";

import { DispatchFailure, GameClient, SubscriptionRunner, actionPending } from "../src";
import type { GameDelta, GameEnvelope, GameProjection, ProjectionReducer, ReconnectAdapter } from "../src";

interface TestView {
  readonly value: number;
}

const envelope = (payload: number): GameEnvelope => ({
  gameId: "liars-dice",
  version: { engine: "1.0.0", protocol: "1.0.0", client: "1.0.0" },
  schemaVersion: 1,
  messageType: "viewer.state",
  payload: new Uint8Array([payload]),
});

const projection = (stateVersion = 1): GameProjection => ({
  kind: "projection",
  sessionId: "session-1",
  stateVersion,
  viewerRole: "player",
  view: envelope(stateVersion),
  allowedActions: ["round.roll"],
});

const delta = (fromStateVersion: number, toStateVersion: number): GameDelta => ({
  kind: "delta",
  sessionId: "session-1",
  fromStateVersion,
  toStateVersion,
  viewerRole: "player",
  messages: [envelope(toStateVersion)],
});

const reducer: ProjectionReducer<TestView> = {
  fromProjection: (value) => ({ value: value.view.payload[0] ?? 0 }),
  applyDelta: (_current, value) => ({ view: { value: value.messages[0]?.payload[0] ?? 0 } }),
};

describe("GameClient", () => {
  it("accepts only a continuous viewer cursor", () => {
    const client = new GameClient<TestView>({ reducer, dispatch: vi.fn() });
    client.accept(projection());
    client.accept(delta(1, 2));
    expect(client.snapshot()).toMatchObject({ stateVersion: 2, view: { value: 2 }, connection: "online" });

    expect(() => client.accept(delta(1, 3))).toThrowError(/does not continue/);
    expect(client.snapshot()).toMatchObject({ stateVersion: 2, connection: "reconnecting", errorCode: "cursor_gap" });
  });

  it("blocks duplicate pending actions and clears pending after the receipt", async () => {
    let resolveDispatch: ((value: { sessionId: string; actionId: string; stateVersion: number; resultCode: string; replayed: boolean }) => void) | undefined;
    const dispatch = vi.fn(
      (command) =>
        new Promise<{ sessionId: string; actionId: string; stateVersion: number; resultCode: string; replayed: boolean }>((resolve) => {
          resolveDispatch = resolve;
        }).then((receipt) => ({ ...receipt, sessionId: command.sessionId, actionId: command.actionId })),
    );
    const client = new GameClient<TestView>({ reducer, dispatch, createActionId: () => "action-1", now: () => 10 });
    client.accept(projection());
    const pending = client.dispatch({ action: "round.roll", message: envelope(1) });

    expect(client.snapshot().pending).toHaveLength(1);
    await expect(client.dispatch({ action: "round.roll", message: envelope(1) })).rejects.toMatchObject(actionPending("round.roll"));
    resolveDispatch?.({ sessionId: "ignored", actionId: "ignored", stateVersion: 2, resultCode: "ok", replayed: false });
    await expect(pending).resolves.toMatchObject({ actionId: "action-1", stateVersion: 2 });
    expect(client.snapshot().pending).toHaveLength(0);
    expect(dispatch).toHaveBeenCalledTimes(1);
  });

  it("retries with the original action id after a retryable failure", async () => {
    const dispatch = vi
      .fn()
      .mockRejectedValueOnce(new DispatchFailure("network", "offline", true))
      .mockImplementationOnce(async (command) => ({
        sessionId: command.sessionId,
        actionId: command.actionId,
        stateVersion: 2,
        resultCode: "ok",
        replayed: true,
      }));
    const client = new GameClient<TestView>({ reducer, dispatch, createActionId: () => "stable-action" });
    client.accept(projection());
    await expect(client.dispatch({ action: "round.roll", message: envelope(1) })).rejects.toMatchObject({ code: "network" });
    await client.retry("round.roll");

    expect(dispatch).toHaveBeenCalledTimes(2);
    expect(dispatch.mock.calls[0]?.[0].actionId).toBe("stable-action");
    expect(dispatch.mock.calls[1]?.[0].actionId).toBe("stable-action");
  });
});

describe("SubscriptionRunner", () => {
  it("reconnects from the last accepted state version", async () => {
    const controller = new AbortController();
    const cursors: Array<number | null> = [];
    const adapter: ReconnectAdapter = {
      connect: async (cursor) => {
        cursors.push(cursor?.stateVersion ?? null);
        if (cursors.length === 1) {
          throw Object.assign(new Error("offline"), { code: "offline" });
        }
        return (async function* () {
          yield projection(2);
          controller.abort();
        })();
      },
    };
    const client = new GameClient<TestView>({ reducer, dispatch: vi.fn() });
    client.accept(projection());
    const runner = new SubscriptionRunner<TestView>({ sleep: async () => undefined });
    await runner.run(client, adapter, controller.signal);

    expect(cursors).toEqual([1, 1]);
    expect(client.snapshot()).toMatchObject({ stateVersion: 2, view: { value: 2 } });
  });
});
