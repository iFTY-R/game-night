import type { RuntimeReadinessState } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";
import { getRuntimeReadiness } from "./admin-auth";

export type ReadinessState = {
  mode: string;
  ready: boolean;
  components: Record<string, string>;
};

export type RuntimeReadiness = {
  ordinary: ReadinessState;
  sensitive: ReadinessState;
};

const readinessState = (state: RuntimeReadinessState | undefined): ReadinessState => {
  if (!state) {
    throw new Error("runtime_readiness_response_incomplete");
  }
  return { mode: state.mode, ready: state.ready, components: { ...state.components } };
};

// Runtime status is exposed only through the authenticated administrator transport.
export const fetchRuntimeReadiness = async (): Promise<RuntimeReadiness> => {
  const response = await getRuntimeReadiness();
  return {
    ordinary: readinessState(response.ordinary),
    sensitive: readinessState(response.sensitive)
  };
};
