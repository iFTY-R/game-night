import { AdminSessionKind, type AdminPermission, type AdminSessionSummary } from "../../../../contracts/gen/ts/platform/admin/v1/admin_auth_pb";

export const formatDateTime = (value?: { seconds?: bigint | number | string } | Date): string => {
  if (!value) {
    return "未提供";
  }
  if (value instanceof Date) {
    return Number.isNaN(value.valueOf()) ? "未提供" : value.toLocaleString("zh-CN");
  }
  const seconds = value.seconds;
  if (seconds == null) {
    return "未提供";
  }
  return new Date(Number(seconds) * 1000).toLocaleString("zh-CN");
};

export const formatSessionKind = (kind?: AdminSessionKind): string => {
  switch (kind) {
    case AdminSessionKind.SETUP_PASSWORD_PENDING:
      return "等待修改初始密码";
    case AdminSessionKind.TOTP_ENROLLMENT_PENDING:
      return "等待绑定验证器";
    case AdminSessionKind.MFA_PENDING:
      return "等待多因素验证";
    case AdminSessionKind.RECOVERY_PENDING:
      return "等待重绑验证器";
    case AdminSessionKind.FULL:
      return "完整会话";
    default:
      return "未知";
  }
};

export const formatPermission = (permission: AdminPermission): string => {
  switch (permission) {
    case 1:
      return "精确用户查询";
    case 2:
      return "读取实名";
    case 3:
      return "修改实名";
    case 7:
      return "封禁用户";
    case 10:
      return "读取审计";
    default:
      return `权限 ${permission}`;
  }
};

export const summarizeSession = (session: AdminSessionSummary | null): string =>
  session ? `${formatSessionKind(session.kind)} · ${session.adminId || "匿名"}` : "未认证";
