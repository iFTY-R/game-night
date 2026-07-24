import { fromBinary, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import type { GameProjection } from "@game-night/game-client";

import {
  CommandSchema,
  ReplaySchema,
  THREE_ROUNDS_REPLAY_MESSAGE,
  THREE_ROUNDS_VERSION,
  ViewSchema,
  createFinishAction,
  createSubmitSelectionAction,
  decodeThreeRoundsReplay,
  resolveSelectionConflict,
  threeRoundsFixtureView,
  threeRoundsReducer,
  threeRoundsReplayFixture,
} from "../src";

const projection = (view = threeRoundsFixtureView("active")): GameProjection => ({
  kind: "projection",
  sessionId: "session-three",
  stateVersion: 7,
  viewerRole: "player",
  allowedActions: view.allowedActions,
  view: { gameId: "three-rounds", version: THREE_ROUNDS_VERSION, schemaVersion: 1, messageType: "session.view", payload: toBinary(ViewSchema, view) },
});

describe("three-rounds protocol", () => {
  it("decodes the stable public selecting projection and accepts platform finish after module actions", () => {
    const view = threeRoundsReducer.fromProjection(projection());
    expect(view.publicPlayers).toHaveLength(4);
    expect(view.currentRound).toBe(1);
    expect(threeRoundsReducer.fromProjection({ ...projection(), allowedActions: [...projection().allowedActions, "session.finish"] }).allowedActions).toEqual(["round.submit_selection"]);
  });

  it("canonicalizes selected card ids by deck order", () => {
    const action = createSubmitSelectionAction(2, ["SJ", "8D"]);
    const command = fromBinary(CommandSchema, action.message.payload);
    expect(command.command.case).toBe("submitSelection");
    if (command.command.case !== "submitSelection") throw new Error("submit selection command expected");
    expect(command.command.value.cardIds).toEqual(["8D", "SJ"]);
  });

  it("treats simultaneous conflicts as confirmed, retryable, or abortable based on the refreshed projection", () => {
    const attempted = createSubmitSelectionAction(1, ["AS"]);
    const pendingView = threeRoundsFixtureView("pending");
    expect(resolveSelectionConflict(pendingView, pendingView.allowedActions, attempted)).toBe("confirmed");

    const retryView = threeRoundsFixtureView("active");
    expect(resolveSelectionConflict(retryView, retryView.allowedActions, attempted)).toBe("retry");

    const terminalView = threeRoundsFixtureView("round-three");
    expect(resolveSelectionConflict(terminalView, terminalView.allowedActions, attempted)).toBe("abort");
  });

  it("decodes replay only for replay viewers and keeps session.finish payload authority-free", () => {
    const replay = threeRoundsReplayFixture();
    const decoded = decodeThreeRoundsReplay({
      kind: "projection",
      sessionId: "session-three",
      stateVersion: 11,
      viewerRole: "replay",
      allowedActions: [],
      view: { gameId: "three-rounds", version: THREE_ROUNDS_VERSION, schemaVersion: 1, messageType: THREE_ROUNDS_REPLAY_MESSAGE, payload: toBinary(ReplaySchema, replay) },
    });
    expect(decoded.rounds).toHaveLength(3);
    expect(decoded.entries.at(-1)?.sequence).toBe(7n);
    expect(createFinishAction().message.payload).toEqual(new Uint8Array());
  });
});
