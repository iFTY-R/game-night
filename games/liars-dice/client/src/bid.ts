import { BidMode, type Bid, type Config, type View } from "./generated/game/liars_dice/v1/liars_dice_pb";
import type { BidDraft, BidValidation } from "./types";

const invalid = (code: string, message: string): BidValidation => ({ valid: false, code, message, risky: false });

// validateBidDraft mirrors the frozen server comparison rules only to explain disabled controls; the server remains authoritative.
export const validateBidDraft = (view: View, draft: BidDraft): BidValidation => {
  const config = view.config;
  if (config === undefined) return invalid("config_missing", "规则配置尚未同步");
  if (!Number.isInteger(draft.quantity) || draft.quantity < 1) return invalid("quantity_invalid", "数量至少为 1");
  if (!Number.isInteger(draft.face) || draft.face < 1 || draft.face > 6) return invalid("face_invalid", "骰面必须在 1 到 6 之间");
  if (draft.mode !== BidMode.FLYING && draft.mode !== BidMode.STRICT) return invalid("mode_invalid", "请选择飞或斋");
  if (draft.mode === BidMode.STRICT && !config.strictEnabled) return invalid("strict_disabled", "本局未开启斋");
  if (draft.face === 1 && draft.mode !== BidMode.STRICT) return invalid("one_requires_strict", "叫 1 点必须使用斋");

  const previous = view.hasCurrentBid ? view.currentBid : undefined;
  if (previous === undefined) {
    if (draft.quantity < config.firstBidMinimum) return invalid("first_bid_low", `首叫至少 ${config.firstBidMinimum} 个`);
  } else {
    const comparison = compareAgainstPrevious(config, previous, draft);
    if (comparison !== null) return comparison;
  }
  const activeDice = view.players.filter((player) => player.active).length * config.dicePerPlayer;
  return {
    valid: true,
    code: "ok",
    message: draft.quantity > activeDice ? `已超过场上 ${activeDice} 颗骰子` : "可以提交",
    risky: draft.quantity > activeDice,
  };
};

const compareAgainstPrevious = (config: Config, previous: Bid, next: BidDraft): BidValidation | null => {
  if (previous.mode === next.mode) {
    if (next.quantity > previous.quantity || (next.quantity === previous.quantity && next.face > previous.face)) return null;
    return invalid("bid_not_higher", "叫骰必须高于上一手");
  }
  if (previous.mode === BidMode.STRICT) {
    if (!config.flyingEnabled) return invalid("flying_disabled", "本局不允许斋后转飞");
    if (previous.face === 1) return invalid("one_cannot_fly", "1 点斋不能直接转飞");
  }
  const anchorQuantity = previous.mode === BidMode.STRICT ? previous.quantity * 2 : Math.floor(previous.quantity / 2) + 1;
  if (next.quantity > anchorQuantity || (next.quantity === anchorQuantity && next.face >= previous.face)) return null;
  return invalid("conversion_low", `切换模式至少 ${anchorQuantity} 个 ${previous.face} 点`);
};

// suggestBid selects a legal low-risk starting draft whenever authoritative round or bid state advances.
export const suggestBid = (view: View): BidDraft => {
  const config = view.config;
  const previous = view.hasCurrentBid ? view.currentBid : undefined;
  if (config === undefined || previous === undefined) {
    return { quantity: Math.max(1, config?.firstBidMinimum ?? 1), face: 2, mode: BidMode.FLYING };
  }
  if (previous.face < 6) return { quantity: previous.quantity, face: previous.face + 1, mode: previous.mode };
  return { quantity: previous.quantity + 1, face: previous.face, mode: previous.mode };
};
