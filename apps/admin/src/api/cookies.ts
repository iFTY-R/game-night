export const ADMIN_CSRF_COOKIE = "gn_admin_csrf";

export const approvedPreferenceKeys = [
  "gn.admin.theme",
  "gn.admin.sidebar-collapsed",
  "gn.admin.tabs"
] as const;

export const readCookie = (name: string): string | undefined => {
  if (typeof document === "undefined") {
    return undefined;
  }
  const prefix = `${name}=`;
  const match = document.cookie
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(prefix));
  return match ? decodeURIComponent(match.slice(prefix.length)) : undefined;
};

export const readAdminCsrfToken = (): string | undefined => readCookie(ADMIN_CSRF_COOKIE);
