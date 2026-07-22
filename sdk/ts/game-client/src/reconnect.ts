import type { GameClient } from "./client";
import { SubscriptionFailure } from "./errors";
import type { ReconnectAdapter, ReconnectPolicy } from "./types";

export interface SubscriptionRunnerOptions {
  readonly policy?: ReconnectPolicy;
  readonly sleep?: (delayMs: number, signal: AbortSignal) => Promise<void>;
}

const defaultPolicy: ReconnectPolicy = {
  initialDelayMs: 250,
  maximumDelayMs: 5_000,
};

// SubscriptionRunner retries transport connections while preserving the last accepted viewer cursor.
export class SubscriptionRunner<TView> {
  readonly #policy: ReconnectPolicy;
  readonly #sleep: (delayMs: number, signal: AbortSignal) => Promise<void>;

  public constructor(options: SubscriptionRunnerOptions = {}) {
    this.#policy = options.policy ?? defaultPolicy;
    this.#sleep = options.sleep ?? sleep;
    if (
      this.#policy.initialDelayMs < 0 ||
      this.#policy.maximumDelayMs < this.#policy.initialDelayMs ||
      !Number.isSafeInteger(this.#policy.initialDelayMs) ||
      !Number.isSafeInteger(this.#policy.maximumDelayMs)
    ) {
      throw new TypeError("Reconnect policy is invalid");
    }
  }

  public async run(client: GameClient<TView>, adapter: ReconnectAdapter, signal: AbortSignal): Promise<void> {
    let delayMs = this.#policy.initialDelayMs;
    while (!signal.aborted) {
      let nextDelayMs = delayMs;
      client.markReconnecting();
      try {
        const updates = await adapter.connect(client.cursor(), signal);
        client.markOnline();
        for await (const update of updates) {
          if (signal.aborted) {
            return;
          }
          client.accept(update);
          delayMs = this.#policy.initialDelayMs;
          nextDelayMs = delayMs;
        }
        if (signal.aborted) return;
        client.markReconnecting("connection_closed");
      } catch (error) {
        if (signal.aborted) {
          return;
        }
        const failure = normalizeSubscriptionFailure(error);
        if (!failure.retryable) {
          client.fail(failure.code);
          return;
        }
        if (failure.phase === "draining") {
          client.markDraining();
        } else {
          client.markReconnecting(failure.code);
        }
        nextDelayMs = failure.retryAfterMs ?? delayMs;
      }
      await this.#sleep(nextDelayMs, signal);
      delayMs = Math.min(Math.max(delayMs * 2, 1), this.#policy.maximumDelayMs);
    }
  }
}

const sleep = async (delayMs: number, signal: AbortSignal): Promise<void> => {
  await new Promise<void>((resolve) => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const finish = (): void => {
      if (timer === undefined) return;
      clearTimeout(timer);
      timer = undefined;
      signal.removeEventListener("abort", finish);
      resolve();
    };
    timer = setTimeout(finish, delayMs);
    signal.addEventListener("abort", finish, { once: true });
    // Abort may have happened between the runner's loop check and listener registration.
    if (signal.aborted) finish();
  });
};

const normalizeSubscriptionFailure = (error: unknown): SubscriptionFailure => {
  if (error instanceof SubscriptionFailure) {
    return error;
  }
  if (typeof error === "object" && error !== null && "code" in error && typeof error.code === "string") {
    return new SubscriptionFailure(error.code, "Subscription connection failed", true, "reconnecting", null, { cause: error });
  }
  return new SubscriptionFailure("connection_lost", "Subscription connection failed", true, "reconnecting", null, { cause: error });
};
