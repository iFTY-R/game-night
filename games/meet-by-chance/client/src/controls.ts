import {
  HandClass,
  MatchKind,
  Phase,
  ResolutionCause,
  RoundOutcome,
  Special235Outcome,
  type Config,
  type MatchBatch,
} from "./generated/game/meet_by_chance/v1/meet_by_chance_pb";

const handLabels: Readonly<Record<number, string>> = {
  [HandClass.SINGLE]: "散骰",
  [HandClass.PAIR]: "对子",
  [HandClass.STRAIGHT]: "顺子",
  [HandClass.LEOPARD]: "豹子",
  [HandClass.SPECIAL_235]: "235",
};

const matchLabels: Readonly<Record<number, string>> = {
  [MatchKind.EXACT]: "完全同牌",
  [MatchKind.HIGHEST]: "多人同大",
  [MatchKind.LOWEST]: "多人同小",
};

const outcomeLabels: Readonly<Record<number, string>> = {
  [RoundOutcome.STOOD]: "靶子结束本轮",
  [RoundOutcome.TARGET_EXCEEDED_ALL]: "靶子超过全场",
  [RoundOutcome.REROLL_LIMIT_REACHED]: "达到重摇上限",
  [RoundOutcome.TARGET_REVOKED]: "靶子离场，本轮取消",
  [RoundOutcome.SESSION_FINISHED]: "整局结束",
};

export const formatTicks = (ticks: number): string => {
  const units = ticks / 4;
  return `${Number.isInteger(units) ? units : units.toFixed(2).replace(/0$/, "")} 单位`;
};

export const handClassLabel = (value: HandClass): string => handLabels[value] ?? "待亮牌";
export const matchKindLabel = (value: MatchKind): string => matchLabels[value] ?? "自动解析";
export const roundOutcomeLabel = (value: RoundOutcome): string => outcomeLabels[value] ?? "已结算";
export const settlementLabel = (outcome: RoundOutcome, cause: ResolutionCause): string => (
  cause === ResolutionCause.TIMEOUT_STAND ? "超时结束本轮" : roundOutcomeLabel(outcome)
);

export const special235Label = (value: Special235Outcome): string | null => {
  if (value === Special235Outcome.BEATS_LEOPARDS) return "235 克制全场豹子";
  if (value === Special235Outcome.MINIMUM_SINGLE) return "235 按全场最小散骰";
  return null;
};

export const phaseLabel = (phase: Phase): string => {
  if (phase === Phase.ROLLING) return "全员摇骰中";
  if (phase === Phase.REVEALING) return "正在公开牌面";
  if (phase === Phase.RESOLVING_MATCH) return "自动解析同牌";
  if (phase === Phase.TARGET_ROLLING) return "靶子重摇中";
  if (phase === Phase.FINISHED) return "本局已结束";
  return "等待靶子决定";
};

export const enabledStraights = (config: Config | undefined): string => {
  if (config === undefined) return "--";
  const values = [
    config.straight123 ? "123" : null,
    config.straight234 ? "234" : null,
    config.straight345 ? "345" : null,
    config.straight456 ? "456" : null,
  ].filter((value): value is string => value !== null);
  return values.join(" / ");
};

export const batchSummary = (batch: MatchBatch | undefined): string | null => {
  if (batch === undefined) return null;
  if (batch.capped) return `同牌解析达到上限 ${batch.resolutionCount}`;
  if (batch.groups.length === 0) return null;
  return batch.groups.map((group) => matchKindLabel(group.kind)).join(" + ");
};
