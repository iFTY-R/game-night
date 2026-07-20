import type { GameClient } from "./client";
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
        }
      } catch (error) {
        if (signal.aborted) {
          return;
        }
        client.markReconnecting(errorCode(error));
      }
      await this.#sleep(delayMs, signal);
      delayMs = Math.min(Math.max(delayMs * 2, 1), this.#policy.maximumDelayMs);
    }
  }
}

const sleep = async (delayMs: number, signal: AbortSignal): Promise<void> => {
  await new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, delayMs);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
};

const errorCode = (error: unknown): string => {
  if (typeof error === "object" && error !== null && "code" in error && typeof error.code === "string") {
    return error.code;
  }
  return "connection_lost";
};
