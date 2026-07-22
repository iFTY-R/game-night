import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import type { GameProjection } from "@game-night/game-client";

import {
  CommandSchema,
  EventSchema,
  HandClassifiedSchema,
  MEET_BY_CHANCE_REPLAY_MESSAGE,
  MEET_BY_CHANCE_VERSION,
  Phase,
  ReplayEntrySchema,
  ReplaySchema,
  ViewSchema,
  createRerollAction,
  createStandAction,
  decodeMeetByChanceReplay,
  meetByChanceFixtureView,
  meetByChanceReducer,
  meetByChanceReplayFixture,
} from "../src";

const projection = (view = meetByChanceFixtureView("active")): GameProjection => ({
  kind: "projection",
  sessionId: "session-meet",
  stateVersion: 12,
  viewerRole: "player",
  allowedActions: view.allowedActions,
  view: { gameId: "meet-by-chance", version: MEET_BY_CHANCE_VERSION, schemaVersion: 1, messageType: "session.view", payload: toBinary(ViewSchema, view) },
});

describe("meet-by-chance protocol", () => {
  it("decodes only the stable public target-decision projection", () => {
    const view = meetByChanceReducer.fromProjection(projection());
    expect(view.publicPlayers).toHaveLength(4);
    expect(view.publicPlayers.every((player) => player.dice.length === 3)).toBe(true);
    expect(view.players).toHaveLength(0);

    const transient = create(ViewSchema, { ...view, phase: Phase.RESOLVING_MATCH, allowedActions: [] });
    expect(() => meetByChanceReducer.fromProjection(projection(transient))).toThrow("meet_by_chance_view_invalid");
  });

  it("accepts platform actions appended after the complete module action set", () => {
    const hostProjection = projection();
    expect(meetByChanceReducer.fromProjection({
      ...hostProjection,
      allowedActions: [...hostProjection.allowedActions, "session.finish"],
    }).allowedActions).toEqual(hostProjection.allowedActions);
  });

  it("rejects missing or reordered module actions", () => {
    const playerProjection = projection();
    expect(() => meetByChanceReducer.fromProjection({
      ...playerProjection,
      allowedActions: playerProjection.allowedActions.slice(1),
    })).toThrow("meet_by_chance_actions_mismatch");
    expect(() => meetByChanceReducer.fromProjection({
      ...playerProjection,
      allowedActions: ["session.finish", ...playerProjection.allowedActions],
    })).toThrow("meet_by_chance_actions_mismatch");
  });

  it("encodes reroll and stand as distinct authoritative commands", () => {
    const reroll = fromBinary(CommandSchema, createRerollAction().message.payload);
    const stand = fromBinary(CommandSchema, createStandAction().message.payload);
    expect(reroll.command.case).toBe("reroll");
    expect(stand.command.case).toBe("stand");
  });

  it("decodes settled replay only for replay viewers with ordered entries", () => {
    const replay = meetByChanceReplayFixture();
    const decoded = decodeMeetByChanceReplay({
      kind: "projection",
      sessionId: "session-meet",
      stateVersion: 18,
      viewerRole: "replay",
      allowedActions: [],
      view: { gameId: "meet-by-chance", version: MEET_BY_CHANCE_VERSION, schemaVersion: 1, messageType: MEET_BY_CHANCE_REPLAY_MESSAGE, payload: toBinary(ReplaySchema, replay) },
    });
    expect(decoded.rounds).toHaveLength(2);
    expect(decoded.entries.at(-1)?.sequence).toBe(15n);

    const invalid = create(ReplaySchema, {
      ...replay,
      entries: replay.entries.map((entry, index) => index === 1 ? create(ReplayEntrySchema, { ...entry, sequence: 9n }) : entry),
    });
    expect(() => decodeMeetByChanceReplay({
      kind: "projection",
      sessionId: "session-meet",
      stateVersion: 18,
      viewerRole: "replay",
      allowedActions: [],
      view: { gameId: "meet-by-chance", version: MEET_BY_CHANCE_VERSION, schemaVersion: 1, messageType: MEET_BY_CHANCE_REPLAY_MESSAGE, payload: toBinary(ReplaySchema, invalid) },
    })).toThrow("meet_by_chance_replay_invalid");
  });

  it("rejects deprecated internal comparison keys in replay events", () => {
    const replay = meetByChanceReplayFixture();
    const classified = replay.entries[2]?.event?.event;
    if (classified?.case !== "handClassified") throw new Error("fixture_hand_classification_missing");
    const leakedEntry = create(ReplayEntrySchema, {
      sequence: replay.entries[2]?.sequence ?? 0n,
      event: create(EventSchema, {
        event: { case: "handClassified", value: create(HandClassifiedSchema, { ...classified.value, fullKey: [4, 6] }) },
      }),
    });
    const leaked = create(ReplaySchema, { ...replay, entries: replay.entries.map((entry, index) => index === 2 ? leakedEntry : entry) });
    expect(() => decodeMeetByChanceReplay({
      kind: "projection",
      sessionId: "session-meet",
      stateVersion: 18,
      viewerRole: "replay",
      allowedActions: [],
      view: { gameId: "meet-by-chance", version: MEET_BY_CHANCE_VERSION, schemaVersion: 1, messageType: MEET_BY_CHANCE_REPLAY_MESSAGE, payload: toBinary(ReplaySchema, leaked) },
    })).toThrow("meet_by_chance_replay_invalid");
  });
});
