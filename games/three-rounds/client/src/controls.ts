import { FinishReason, HandClass, Phase, type Config, type FinalStanding, type PlayerReveal, type PublicPlayer, type RoundSummary } from "./generated/game/three_rounds/v1/three_rounds_pb";
import { formatCardLabel } from "./cards";

export const selectionLimitForPhase = (phase: Phase): 0 | 1 | 2 =>
  phase === Phase.ROUND_ONE_SELECTING ? 1 : phase === Phase.ROUND_TWO_SELECTING ? 2 : 0;

export const phaseLabel = (phase: Phase): string => {
  if (phase === Phase.DEALING) return "正在发牌";
  if (phase === Phase.ROUND_ONE_SELECTING) return "第一关选一张";
  if (phase === Phase.ROUND_ONE_RESULT) return "第一关结果";
  if (phase === Phase.ROUND_TWO_SELECTING) return "第二关选两张";
  if (phase === Phase.ROUND_TWO_RESULT) return "第二关结果";
  if (phase === Phase.ROUND_THREE_RESULT) return "第三关自动结算";
  if (phase === Phase.FINAL_RESULT) return "总结果";
  if (phase === Phase.FINISHED) return "本局结束";
  return "等待同步";
};

export const roundLabel = (round: number): string => round === 1 ? "第一关" : round === 2 ? "第二关" : "第三关";

export const handClassLabel = (value: HandClass): string => {
  if (value === HandClass.TRIPS) return "豹子";
  if (value === HandClass.STRAIGHT_FLUSH) return "顺金";
  if (value === HandClass.FLUSH) return "金花";
  if (value === HandClass.STRAIGHT) return "顺子";
  if (value === HandClass.PAIR) return "对子";
  return "单张";
};

export const finishReasonLabel = (reason: FinishReason): string => {
  if (reason === FinishReason.NORMAL_COMPLETED) return "正常完成";
  if (reason === FinishReason.HOST_REQUESTED) return "房主取消";
  if (reason === FinishReason.INSUFFICIENT_PARTICIPANTS) return "人数不足";
  return "未说明";
};

export const formatHalfPoints = (totalHalfPoints: number): string =>
  totalHalfPoints % 2 === 0 ? String(totalHalfPoints / 2) : `${Math.floor(totalHalfPoints / 2)}.5`;

export const roundResultLabel = (summary: RoundSummary): string => {
  if (summary.round === 2 && summary.allBusted) return "全员爆牌";
  const winners = summary.winnerUserIds.length;
  return winners > 1 ? `${winners} 人并列领先` : "单人领先";
};

export const standingBadge = (standing: FinalStanding): string =>
  standing.rank <= 1 ? "冠军" : `第 ${standing.rank} 名`;

export const playerRoundStatus = (player: PublicPlayer, activeRound: number): string => {
  if (!player.active) return "已离场";
  if (activeRound === 1 && player.submitted) return "已确认";
  if (activeRound === 2 && player.submitted) return "已确认";
  if (player.finalWinner) return "胜出";
  if (player.finalRank > 0) return `第 ${player.finalRank} 名`;
  return "等待中";
};

export const revealSummary = (reveal: PlayerReveal, round: number): string => {
  if (round === 1) return `${formatCardLabel(reveal.roundOne?.cardId ?? reveal.cardIds[0] ?? "")} · ${reveal.awardedPoints} 分`;
  if (round === 2) return `${formatHalfPoints(reveal.roundTwo?.totalHalfPoints ?? 0)} 点 · ${reveal.roundTwo?.busted ? "爆牌" : `${reveal.awardedPoints} 分`}`;
  const label = reveal.roundThree ? handClassLabel(reveal.roundThree.handClass) : "未公开";
  return `${label} · ${reveal.awardedPoints} 分`;
};

export const configSummary = (config?: Config): readonly string[] => {
  if (config === undefined) return [];
  return [
    `第一关 ${config.roundOneTimeoutSeconds} 秒`,
    `第二关 ${config.roundTwoTimeoutSeconds} 秒`,
    `结果停留 ${config.roundResultSeconds} 秒`,
    `总榜停留 ${config.finalResultSeconds} 秒`,
  ];
};
