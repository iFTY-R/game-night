/** Returns one server-owned member name, with a viewer-independent fallback for legacy or incomplete snapshots. */
export const memberDisplayName = (userId: string, username?: string): string => {
  const authoritativeName = username?.trim();
  if (authoritativeName) return authoritativeName;
  const stableId = userId.slice(0, 6);
  return stableId ? `玩家 ${stableId}` : "未知玩家";
};
