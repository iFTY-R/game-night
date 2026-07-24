export const gameCatalog = [
  {
    gameId: "liars-dice",
    name: "吹牛骰子",
    summary: "轮流叫骰，判断上一手是真还是吹牛。",
    minimumPlayers: 2,
    accent: "叫",
  },
  {
    gameId: "dice-789",
    name: "789",
    summary: "两颗骰子围绕公共令杯触发加酒、半杯和喝完。",
    minimumPlayers: 2,
    accent: "789",
  },
  {
    gameId: "meet-by-chance",
    name: "骰子喜相逢",
    summary: "三骰比大小，同牌相逢并由靶子决定重摇或停手。",
    minimumPlayers: 3,
    accent: "喜",
  },
  {
    gameId: "three-rounds",
    name: "三关定胜负",
    summary: "六张手牌分三关结算，前两关手选，第三关自动亮底牌。",
    minimumPlayers: 2,
    accent: "3关",
  },
] as const;

export type GameId = (typeof gameCatalog)[number]["gameId"];

export const isGameId = (value: string): value is GameId => gameCatalog.some((game) => game.gameId === value);

export const gameById = (gameId: string) => gameCatalog.find((game) => game.gameId === gameId);
