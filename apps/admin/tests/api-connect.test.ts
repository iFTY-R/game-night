import { describe, expect, it, vi } from "vitest";
import { getRuntimeReadiness, getSetupState, loginPassword, verifyTotp } from "../src/api/admin-auth";
import { lookupUser } from "../src/api/admin-identity";
import { installSessionInvalidHandler } from "../src/api/connect";
import { AdminApiError } from "../src/api/errors";

describe("admin connect transport", () => {
  it("posts admin auth requests with relative path and flow/csrf headers", async () => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ nextStep: "ADMIN_NEXT_STEP_VERIFY_MFA" }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await loginPassword({
      challengeProof: "challenge-proof",
      password: "secret-password",
      requestFlowId: "flow-1"
    });

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminAuthService/LoginPassword");
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(headers.get("X-Request-Flow-ID")).toBe("flow-1");
    expect(headers.get("Connect-Protocol-Version")).toBe("1");
    expect(init.credentials).toBe("include");
    expect(String(init.body)).toContain("challengeProof");
  });

  it("assigns request ids only to identity procedures", async () => {
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ user: { userId: "u-1", status: "USER_STATUS_ACTIVE", username: "alpha" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await lookupUser({ case: "username", value: "alpha" });

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminIdentityService/GetUser");
    expect(headers.get("X-Request-ID")).toMatch(/[0-9a-f-]{8,}/i);
  });

  it("requests runtime readiness through the authenticated admin service", async () => {
    Object.defineProperty(document, "cookie", {
      configurable: true,
      writable: true,
      value: "__Host-gn_admin_csrf=csrf-token"
    });
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ordinary: { mode: "ordinary", ready: true, components: { postgresql: "ready" } },
          sensitive: { mode: "sensitive_write", ready: false, components: { redis: "unavailable" } }
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      )
    );
    vi.stubGlobal("fetch", fetchSpy);

    const response = await getRuntimeReadiness();

    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(url).toBe("/platform.admin.v1.AdminAuthService/GetRuntimeReadiness");
    expect(headers.get("X-CSRF-Token")).toBe("csrf-token");
    expect(response.ordinary?.components.postgresql).toBe("ready");
    expect(response.sensitive?.ready).toBe(false);
  });

  it("invokes the session invalid handler on stable auth errors", async () => {
    const invalidSpy = vi.fn();
    installSessionInvalidHandler(invalidSpy);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            code: "unauthenticated",
            message: "invalid session",
            details: [
              {
                type: "type.googleapis.com/platform.common.v1.BusinessErrorDetail",
                value: {
                  code: "BUSINESS_ERROR_CODE_AUTH_INVALID",
                  messageKey: "admin.auth.invalid",
                  fieldViolations: []
                }
              }
            ]
          }),
          { status: 401, headers: { "Content-Type": "application/json" } }
        )
      )
    );

    await expect(verifyTotp("000000")).rejects.toBeInstanceOf(AdminApiError);
    expect(invalidSpy).toHaveBeenCalledTimes(1);
  });

  it("supports anonymous setup requests without csrf", async () => {
    const fetchSpy = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ state: "ADMIN_SETUP_STATE_ACTIVE" }), {
        status: 200,
        headers: { "Content-Type": "application/json" }
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    await getSetupState();

    const setupHeaders = new Headers((fetchSpy.mock.calls[0] as [string, RequestInit])[1].headers);
    expect(setupHeaders.get("X-CSRF-Token")).toBeNull();
  });
});
