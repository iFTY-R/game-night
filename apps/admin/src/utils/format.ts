import { AdminPermission, AdminSessionKind, type AdminSessionSummary } from "../../../../contracts/gen/ts/platform/admin/v1/admin_common_pb";

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
    case AdminSessionKind.MFA_PENDING:
      return "等待多因素验证";
    case AdminSessionKind.FULL:
      return "完整会话";
    default:
      return "未知";
  }
};

export const formatPermission = (permission: AdminPermission): string => {
  switch (permission) {
    case AdminPermission.SECURITY_READ:
      return "读取安全设置";
    case AdminPermission.SECURITY_MANAGE_PASSWORD:
      return "管理密码";
    case AdminPermission.SECURITY_MANAGE_MFA:
      return "管理多因素认证";
    case AdminPermission.SECURITY_MANAGE_SESSIONS:
      return "管理会话";
    default:
      return `权限 ${permission}`;
  }
};

export const summarizeSession = (session: AdminSessionSummary | null): string =>
  session ? `${formatSessionKind(session.kind)} · ${session.adminId || "匿名"}` : "未认证";
