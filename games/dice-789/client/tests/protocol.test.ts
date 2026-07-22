import { fromBinary, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import type { GameProjection } from "@game-night/game-client";

import {
  CommandSchema,
  ReplaySchema,
  ViewSchema,
  createAddAction,
  createDroppedAction,
  createTargetAction,
  decodeDice789Replay,
  dice789FixtureView,
  dice789Reducer,
  dice789ReplayFixture,
} from "../src";
import { DICE_789_REPLAY_MESSAGE, DICE_789_VERSION } from "../src/constants";

const projection = (): GameProjection => {
  const view = dice789FixtureView("add");
  return {
    kind: "projection",
    sessionId: "session-789",
    stateVersion: 7,
    viewerRole: "player",
    allowedActions: view.allowedActions,
    view: { gameId: "dice-789", version: DICE_789_VERSION, schemaVersion: 1, messageType: "session.view", payload: toBinary(ViewSchema, view) },
  };
};

describe("dice789 protocol", () => {
  it("decodes the exact view envelope and frozen action constraints", () => {
    const view = dice789Reducer.fromProjection(projection());
    expect(view.actionConstraints?.maximumAddTicks).toBe(5);
    expect(view.config?.doubleFourEnabled).toBe(true);
  });

  it("accepts platform actions appended after the complete module action set", () => {
    const hostProjection = projection();
    expect(dice789Reducer.fromProjection({
      ...hostProjection,
      allowedActions: [...hostProjection.allowedActions, "session.finish"],
    }).allowedActions).toEqual(hostProjection.allowedActions);
  });

  it("rejects missing or reordered module actions", () => {
    const playerProjection = projection();
    expect(() => dice789Reducer.fromProjection({
      ...playerProjection,
      allowedActions: playerProjection.allowedActions.slice(1),
    })).toThrow("dice_789_actions_mismatch");
    expect(() => dice789Reducer.fromProjection({
      ...playerProjection,
      allowedActions: ["session.finish", ...playerProjection.allowedActions],
    })).toThrow("dice_789_actions_mismatch");
  });

  it("encodes amount, target, and dropped reason inside command digests", () => {
    const add = fromBinary(CommandSchema, createAddAction(5).message.payload);
    const target = fromBinary(CommandSchema, createTargetAction("user-qing").message.payload);
    const dropped = fromBinary(CommandSchema, createDroppedAction("left_cup").message.payload);
    expect(add.command.case === "addToPool" ? add.command.value.ticks : 0).toBe(5);
    expect(target.command.case === "chooseTarget" ? target.command.value.userId : "").toBe("user-qing");
    expect(dropped.command.case === "reportDropped" ? dropped.command.value.reason : "").toBe("left_cup");
  });

  it("decodes immutable replay turns only for replay viewers", () => {
    const replay = dice789ReplayFixture();
    const decoded = decodeDice789Replay({
      kind: "projection",
      sessionId: "session-789",
      stateVersion: 12,
      viewerRole: "replay",
      allowedActions: [],
      view: { gameId: "dice-789", version: DICE_789_VERSION, schemaVersion: 1, messageType: DICE_789_REPLAY_MESSAGE, payload: toBinary(ReplaySchema, replay) },
    });
    expect(decoded.turns).toHaveLength(2);
    expect(decoded.turns[1]?.summary?.effect).toBeGreaterThan(0);
  });
});
