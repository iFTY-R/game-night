export type TableShape = "adaptive" | "compact-oval" | "elongated-oval" | "rounded-table";
export type TrayState = "collapsed" | "compact" | "expanded";
export type ConnectionState = "online" | "reconnecting" | "offline" | "draining";

export interface TableSeat {
  readonly seatIndex: number;
  readonly userId: string;
  readonly displayName: string;
  readonly avatarText?: string;
  readonly status?: string;
  readonly connected: boolean;
  readonly active?: boolean;
  readonly host?: boolean;
}

export interface SeatPosition {
  readonly seatIndex: number;
  readonly x: number;
  readonly y: number;
  readonly angle: number;
}
