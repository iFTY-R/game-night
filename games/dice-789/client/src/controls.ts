import type { ActionConstraints } from "./generated/game/dice789/v1/dice_789_pb";

// Legal values mirror server step and final-capacity remainder rules without classifying any dice effect.
export const legalAddValues = (constraints: ActionConstraints | undefined): readonly number[] => {
  if (constraints === undefined || constraints.maximumAddTicks === 0) return [0];
  const values: number[] = [];
  const step = Math.max(1, constraints.addStepTicks);
  for (let value = step; value <= constraints.maximumAddTicks; value += step) {
    if (value >= constraints.minimumAddTicks) values.push(value);
  }
  if (!values.includes(constraints.maximumAddTicks)) values.push(constraints.maximumAddTicks);
  return values.sort((left, right) => left - right);
};

export const formatTicks = (ticks: number): string => {
  const whole = Math.floor(ticks / 4);
  const remainder = ticks % 4;
  const fraction = ["", ".25", ".5", ".75"][remainder] ?? "";
  return `${whole}${fraction} 单位`;
};
