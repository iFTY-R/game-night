const suits = {
  D: { label: "方块", glyph: "♦", tone: "warm" },
  C: { label: "梅花", glyph: "♣", tone: "cool" },
  H: { label: "红桃", glyph: "♥", tone: "warm" },
  S: { label: "黑桃", glyph: "♠", tone: "cool" },
} as const;

const orderedCardIds = [
  "2D", "2C", "2H", "2S",
  "3D", "3C", "3H", "3S",
  "4D", "4C", "4H", "4S",
  "5D", "5C", "5H", "5S",
  "6D", "6C", "6H", "6S",
  "7D", "7C", "7H", "7S",
  "8D", "8C", "8H", "8S",
  "9D", "9C", "9H", "9S",
  "10D", "10C", "10H", "10S",
  "JD", "JC", "JH", "JS",
  "QD", "QC", "QH", "QS",
  "KD", "KC", "KH", "KS",
  "AD", "AC", "AH", "AS",
  "SJ", "BJ",
] as const;

const ordinalByCardId = new Map<string, number>(orderedCardIds.map((cardId, index) => [cardId, index]));
const jokerNames = {
  SJ: { rank: "小王", suit: "王", glyph: "☆" },
  BJ: { rank: "大王", suit: "王", glyph: "★" },
} as const;

export interface ParsedCard {
  readonly cardId: string;
  readonly ordinal: number;
  readonly rankLabel: string;
  readonly suitLabel: string;
  readonly suitGlyph: string;
  readonly suitTone: "warm" | "cool" | "joker";
  readonly joker: boolean;
}

/** Parses the frozen card identifiers from the rules spec into UI-safe labels and sort keys. */
export const parseCard = (cardId: string): ParsedCard | null => {
  const normalized = cardId.trim();
  const ordinal = ordinalByCardId.get(normalized);
  if (ordinal === undefined) return null;
  if (normalized === "SJ" || normalized === "BJ") {
    const joker = jokerNames[normalized];
    return {
      cardId: normalized,
      ordinal,
      rankLabel: joker.rank,
      suitLabel: joker.suit,
      suitGlyph: joker.glyph,
      suitTone: "joker",
      joker: true,
    };
  }
  const suitKey = normalized.slice(-1) as keyof typeof suits;
  const rankLabel = normalized.slice(0, -1);
  const suit = suits[suitKey];
  if (!rankLabel || suit === undefined) return null;
  return {
    cardId: normalized,
    ordinal,
    rankLabel,
    suitLabel: suit.label,
    suitGlyph: suit.glyph,
    suitTone: suit.tone,
    joker: false,
  };
};

export const cardOrdinal = (cardId: string): number => ordinalByCardId.get(cardId.trim()) ?? Number.MAX_SAFE_INTEGER;

/** Canonical payloads and replay comparisons always sort selections by the frozen deck ordinal. */
export const sortCardIdsByOrdinal = (cardIds: readonly string[]): string[] => [...cardIds].sort((left, right) => cardOrdinal(left) - cardOrdinal(right));

export const formatCardLabel = (cardId: string): string => {
  const parsed = parseCard(cardId);
  return parsed === null ? cardId : parsed.joker ? parsed.rankLabel : `${parsed.rankLabel}${parsed.suitGlyph}`;
};

export const isKnownCardId = (cardId: string): boolean => ordinalByCardId.has(cardId.trim());
