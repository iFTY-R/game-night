import type { FieldViolation } from "../../../../contracts/gen/ts/platform/common/v1/common_pb";

export class AdminApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly businessKey: string;
  readonly fieldViolations: FieldViolation[];
  readonly retryAt?: Date;

  constructor(input: {
    message: string;
    status: number;
    code: string;
    businessKey: string;
    fieldViolations?: FieldViolation[];
    retryAt?: Date;
  }) {
    super(input.message);
    this.name = "AdminApiError";
    this.status = input.status;
    this.code = input.code;
    this.businessKey = input.businessKey;
    this.fieldViolations = input.fieldViolations ?? [];
    if (input.retryAt) {
      this.retryAt = input.retryAt;
    }
  }
}

export const isSessionInvalidError = (error: unknown): boolean =>
  error instanceof AdminApiError &&
  (error.businessKey === "admin.auth.invalid" || error.businessKey === "request.csrf.invalid");
