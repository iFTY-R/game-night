// Shared browser-side onboarding hint. The API remains authoritative, but the UI mirrors the same syntax gate.
export const USERNAME_RULE_MESSAGE = "2-4 个汉字、英文字母或数字";

const MIN_USERNAME_CODE_POINTS = 2;
const MAX_USERNAME_CODE_POINTS = 4;

const controlOrFormatPattern = /[\p{Cc}\p{Cf}]/u;
const decimalPattern = /\p{Decimal_Number}/u;
const letterPattern = /\p{Letter}/u;
const latinPattern = /\p{Script=Latin}/u;
const hanPattern = /\p{Script=Han}/u;

export interface UsernameValidationResult {
  readonly normalized: string;
  readonly codePointCount: number;
  readonly isValid: boolean;
}

/** Mirrors the backend's NFKC-plus-trim display normalization so the form previews the exact submitted spelling. */
export const normalizeUsernameInput = (value: string): string => value.normalize("NFKC").trim();

/** Validates the public username syntax only; reserved-name and sensitive-fragment policy remains server-owned. */
export const validateUsernameInput = (value: string): UsernameValidationResult => {
  if (controlOrFormatPattern.test(value)) {
    return { normalized: "", codePointCount: 0, isValid: false };
  }
  const normalized = normalizeUsernameInput(value);
  const codePointCount = [...normalized].length;
  if (codePointCount < MIN_USERNAME_CODE_POINTS || codePointCount > MAX_USERNAME_CODE_POINTS) {
    return { normalized, codePointCount, isValid: false };
  }
  for (const character of normalized) {
    if (!isAllowedUsernameCharacter(character)) {
      return { normalized, codePointCount, isValid: false };
    }
  }
  return { normalized, codePointCount, isValid: true };
};

const isAllowedUsernameCharacter = (character: string): boolean => {
  if (decimalPattern.test(character)) {
    return true;
  }
  return letterPattern.test(character) && (latinPattern.test(character) || hanPattern.test(character));
};
