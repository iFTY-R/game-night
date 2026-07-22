import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import type { GameProjection } from "@game-night/game-client";

import {
  BidMode,
  CommandSchema,
  ViewSchema,
  createBidAction,
  decodeLiarsDiceReplay,
  liarsDiceFixtureView,
  liarsDiceReplayFixture,
  liarsDiceReducer,
} from "../src";
import { LIARS_DICE_REPLAY_MESSAGE, LIARS_DICE_VERSION } from "../src/constants";
import { ReplaySchema } from "../src/generated/game/liars_dice/v1/liars_dice_pb";

const projection = (viewerRole: "player" | "spectator", ownDice = true): GameProjection => {
  const view = liarsDiceFixtureView();
  const safeView = ownDice ? view : create(ViewSchema, { ...view, ownDice: [] });
  return {
    kind: "projection",
    sessionId: "session-1",
    stateVersion: 3,
    viewerRole,
    allowedActions: safeView.allowedActions,
    view: {
      gameId: "liars-dice",
      version: LIARS_DICE_VERSION,
      schemaVersion: 1,
      messageType: "session.view",
      payload: toBinary(ViewSchema, safeView),
    },
  };
};

describe("liarsDiceReducer", () => {
  it("decodes the exact registered viewer envelope", () => {
    const view = liarsDiceReducer.fromProjection(projection("player"));
    expect(view.ownDice).toEqual([2, 4, 4, 5, 6]);
    expect(view.config?.onesWild).toBe(true);
  });

  it("rejects private dice in a spectator projection", () => {
    expect(() => liarsDiceReducer.fromProjection(projection("spectator"))).toThrow("liars_dice_private_dice_leak");
    expect(liarsDiceReducer.fromProjection(projection("spectator", false)).ownDice).toEqual([]);
  });

  it("accepts platform actions appended to the module action set", () => {
    const hostProjection = projection("player");

    expect(liarsDiceReducer.fromProjection({
      ...hostProjection,
      allowedActions: [...hostProjection.allowedActions, "session.finish"],
    }).allowedActions).toEqual(hostProjection.allowedActions);
  });

  it("rejects missing or reordered module actions", () => {
    const playerProjection = projection("player");

    expect(() => liarsDiceReducer.fromProjection({
      ...playerProjection,
      allowedActions: playerProjection.allowedActions.slice(1),
    })).toThrow("liars_dice_actions_mismatch");
    expect(() => liarsDiceReducer.fromProjection({
      ...playerProjection,
      allowedActions: ["session.finish", ...playerProjection.allowedActions],
    })).toThrow("liars_dice_actions_mismatch");
  });

  it("encodes a version-pinned bid command", () => {
    const action = createBidAction({ quantity: 7, face: 5, mode: BidMode.FLYING });
    const command = fromBinary(CommandSchema, action.message.payload);
    expect(action.action).toBe("round.bid");
    expect(command.command.case).toBe("placeBid");
    if (command.command.case === "placeBid") expect(command.command.value.bid?.quantity).toBe(7);
  });

  it("decodes a read-only replay with the complete settled bid chain", () => {
    const replay = liarsDiceReplayFixture();
    const decoded = decodeLiarsDiceReplay({
      kind: "projection",
      sessionId: "session-1",
      stateVersion: 9,
      viewerRole: "replay",
      allowedActions: [],
      view: {
        gameId: "liars-dice",
        version: LIARS_DICE_VERSION,
        schemaVersion: 1,
        messageType: LIARS_DICE_REPLAY_MESSAGE,
        payload: toBinary(ReplaySchema, replay),
      },
    });
    expect(decoded.players).toHaveLength(4);
    expect(decoded.players[3]).toMatchObject({ userId: "user-self", seatIndex: 3 });
    expect(decoded.rounds[0]?.bids).toHaveLength(4);
    expect(decoded.rounds[0]?.dice).toHaveLength(4);
  });
});
