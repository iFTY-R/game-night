const textEncoder = new TextEncoder();
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const maximumUint64 = (1n << 64n) - 1n;

interface MessageDigestInput {
  messageType: string;
  schemaVersion: number;
  payload: Uint8Array;
}

interface VersionedMessageDigestInput extends MessageDigestInput {
  gameId: string;
  version: { engine: string; protocol: string; client: string };
}

interface StartDigestInput {
  actorUserId: string;
  roomId: string;
  operationId: string;
  gameId: string;
  roomVersion: string;
  membershipVersion: string;
  configRevision: string | number | bigint;
  config: MessageDigestInput;
}

interface ActionDigestInput {
  sessionId: string;
  actorUserId: string;
  actionId: string;
  expectedStateVersion: number;
  command: VersionedMessageDigestInput;
}

interface FinishDigestInput {
  sessionId: string;
  actorUserId: string;
  operationId: string;
  sourceEventId: string;
  expectedStateVersion: number;
  command: VersionedMessageDigestInput;
}

interface RuleRoomDigestInput {
  roomId: string;
  operationId: string;
  roomVersion: string;
  membershipVersion: string;
  ownershipEpoch: string | number | bigint;
}

interface SelectRoomGameDigestInput extends RuleRoomDigestInput {
  gameId: string;
}

interface UpdateGameConfigDigestInput extends SelectRoomGameDigestInput {
  expectedRevision: string | number | bigint;
  config: VersionedMessageDigestInput;
}

interface SaveGameRulePresetDigestInput {
  presetId: string;
  gameId: string;
  name: string;
  mode: string;
  expectedPresetRevision: string | number | bigint;
  operationId: string;
  config: VersionedMessageDigestInput;
}

interface DeleteGameRulePresetDigestInput {
  presetId: string;
  expectedPresetRevision: string | number | bigint;
  operationId: string;
}

interface BeginGameStartDigestInput extends SelectRoomGameDigestInput {
  configRevision: string | number | bigint;
}

interface CancelGameStartDigestInput extends RuleRoomDigestInput {
  pendingStartId: string;
  cancelToken: string;
}

const textBytes = (value: string): Uint8Array => textEncoder.encode(value);

const uint64Bytes = (value: string | number | bigint): Uint8Array => {
  const parsed = BigInt(value);
  if (parsed < 0n || parsed > maximumUint64) {
    throw new RangeError("operation_digest_uint64_invalid");
  }
  const encoded = new Uint8Array(8);
  new DataView(encoded.buffer).setBigUint64(0, parsed, false);
  return encoded;
};

const uuidBytes = (value: string): Uint8Array => {
  if (!uuidPattern.test(value)) {
    throw new TypeError("operation_digest_uuid_invalid");
  }
  const compact = value.replaceAll("-", "");
  return Uint8Array.from({ length: 16 }, (_, index) => Number.parseInt(compact.slice(index * 2, index * 2 + 2), 16));
};

const concatBytes = (parts: Uint8Array[]): Uint8Array => {
  const result = new Uint8Array(parts.reduce((length, part) => length + part.length, 0));
  let offset = 0;
  for (const part of parts) {
    result.set(part, offset);
    offset += part.length;
  }
  return result;
};

// The Go runtime hashes every field as an eight-byte big-endian length followed by the field bytes.
const digestFields = async (fields: Uint8Array[]): Promise<string> => {
  const framed = concatBytes(fields.flatMap((field) => [uint64Bytes(field.length), field]));
  const framedBuffer = new ArrayBuffer(framed.byteLength);
  new Uint8Array(framedBuffer).set(framed);
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", framedBuffer));
  let binary = "";
  for (const byte of digest) binary += String.fromCharCode(byte);
  return btoa(binary);
};

const messageFields = (message: MessageDigestInput): Uint8Array[] => [
  textBytes(message.messageType),
  uint64Bytes(message.schemaVersion),
  message.payload,
];

const versionFields = (message: VersionedMessageDigestInput): Uint8Array[] => [
  textBytes(message.gameId),
  textBytes(message.version.engine),
  textBytes(message.version.protocol),
  textBytes(message.version.client),
];

const roomRuleFields = (input: RuleRoomDigestInput): Uint8Array[] => [
  textBytes(input.roomId),
  textBytes(input.operationId),
  uint64Bytes(input.roomVersion),
  uint64Bytes(input.membershipVersion),
  uint64Bytes(input.ownershipEpoch),
];

/** Reproduces game-runtime.startDigest so the server can reject tampered start requests. */
export const startRequestDigest = (input: StartDigestInput): Promise<string> => digestFields([
  uuidBytes(input.actorUserId),
  uuidBytes(input.roomId),
  textBytes(input.operationId),
  textBytes(input.gameId),
  uint64Bytes(input.roomVersion),
  uint64Bytes(input.membershipVersion),
  uint64Bytes(input.configRevision),
  ...messageFields(input.config),
]);

/** Reproduces game-runtime.actionDigest including the authenticated player identity. */
export const actionRequestDigest = (input: ActionDigestInput): Promise<string> => digestFields([
  uuidBytes(input.sessionId),
  uuidBytes(input.actorUserId),
  textBytes(input.actionId),
  uint64Bytes(input.expectedStateVersion),
  ...versionFields(input.command),
  ...messageFields(input.command),
]);

/** Reproduces the host_api branch of game-runtime.systemDigest used to finish a room session. */
export const finishRequestDigest = (input: FinishDigestInput): Promise<string> => digestFields([
  uuidBytes(input.sessionId),
  textBytes(input.operationId),
  textBytes("host_api"),
  uuidBytes(input.sourceEventId),
  uuidBytes(input.actorUserId),
  ...versionFields(input.command),
  uint64Bytes(input.expectedStateVersion),
  ...messageFields(input.command),
]);

/** Binds a host-only game selection request to the room version and ownership epoch it observed. */
export const selectRoomGameRequestDigest = (input: SelectRoomGameDigestInput): Promise<string> => digestFields([
  textBytes("room.select_game"),
  ...roomRuleFields(input),
  textBytes(input.gameId),
]);

/** Binds one complete rules envelope update so operation replays cannot swap the payload. */
export const updateGameConfigRequestDigest = (input: UpdateGameConfigDigestInput): Promise<string> => digestFields([
  textBytes("room.update_game_config"),
  ...roomRuleFields(input),
  textBytes(input.gameId),
  uint64Bytes(input.expectedRevision),
  ...versionFields(input.config),
  ...messageFields(input.config),
]);

/** Binds a personal preset create/overwrite/copy operation to its selected mode and envelope. */
export const saveGameRulePresetRequestDigest = (input: SaveGameRulePresetDigestInput): Promise<string> => digestFields([
  textBytes("room.save_game_rule_preset"),
  textBytes(input.operationId),
  textBytes(input.presetId),
  textBytes(input.gameId),
  textBytes(input.name),
  textBytes(input.mode),
  uint64Bytes(input.expectedPresetRevision),
  ...versionFields(input.config),
  ...messageFields(input.config),
]);

/** Binds personal preset deletion to the optimistic preset revision seen by the UI. */
export const deleteGameRulePresetRequestDigest = (input: DeleteGameRulePresetDigestInput): Promise<string> => digestFields([
  textBytes("room.delete_game_rule_preset"),
  textBytes(input.operationId),
  textBytes(input.presetId),
  uint64Bytes(input.expectedPresetRevision),
]);

/** Binds countdown creation to the selected draft revision and room ownership epoch. */
export const beginGameStartRequestDigest = (input: BeginGameStartDigestInput): Promise<string> => digestFields([
  textBytes("room.begin_game_start"),
  ...roomRuleFields(input),
  textBytes(input.gameId),
  uint64Bytes(input.configRevision),
]);

/** Binds countdown cancellation to the opaque pending-start token issued by the server. */
export const cancelGameStartRequestDigest = (input: CancelGameStartDigestInput): Promise<string> => digestFields([
  textBytes("room.cancel_game_start"),
  ...roomRuleFields(input),
  textBytes(input.pendingStartId),
  textBytes(input.cancelToken),
]);
