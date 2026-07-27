import { create } from "@bufbuild/protobuf";
import { TimestampSchema } from "@bufbuild/protobuf/wkt";
import {
  AdminOverviewGranularity,
  AdminOverviewService,
  GetOverviewRequestSchema,
  GetOverviewResponseSchema,
  type GetOverviewResponse
} from "../../../../contracts/gen/ts/platform/admin/v1/admin_overview_pb";
import { callUnary, procedure, type UnaryRequestPolicy } from "./connect";

const serviceName = "platform.admin.v1.AdminOverviewService";
const sessionRead = { csrf: true, requestId: false } as const satisfies UnaryRequestPolicy;
const getOverviewMethod = procedure(serviceName, "GetOverview", GetOverviewRequestSchema, GetOverviewResponseSchema, sessionRead);

const toTimestamp = (value: Date) => {
  const milliseconds = value.getTime();
  const seconds = Math.floor(milliseconds / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: (milliseconds - seconds * 1000) * 1_000_000 });
};

export type GetOverviewInput = {
  windowStart: Date;
  windowEnd: Date;
  granularity: AdminOverviewGranularity;
  signal?: AbortSignal;
};

/** Reads a UTC-aligned real overview window; callers own request cancellation and stale-result guards. */
export const getOverview = (input: GetOverviewInput): Promise<GetOverviewResponse> =>
  callUnary<GetOverviewResponse>(
    getOverviewMethod,
    {
      windowStart: toTimestamp(input.windowStart),
      windowEnd: toTimestamp(input.windowEnd),
      granularity: input.granularity
    },
    input.signal ? { signal: input.signal } : undefined
  );

export { AdminOverviewGranularity, AdminOverviewService };
