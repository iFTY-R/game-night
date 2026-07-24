import type { GlobalThemeOverrides } from "naive-ui";

export type ResolvedTheme = "light" | "dark";

export const naiveThemeOverrides = (theme: ResolvedTheme): GlobalThemeOverrides => {
  const shared = {
    common: {
      borderRadius: "8px",
      borderRadiusSmall: "6px",
      fontFamily: '"Avenir Next", "Noto Sans SC", "PingFang SC", "Microsoft YaHei", sans-serif',
      primaryColor: theme === "dark" ? "#4db8ae" : "#16766f",
      primaryColorHover: theme === "dark" ? "#67c8bf" : "#10655f",
      primaryColorPressed: theme === "dark" ? "#3ca49b" : "#0d5752",
      primaryColorSuppl: theme === "dark" ? "#67c8bf" : "#10655f",
      successColor: theme === "dark" ? "#55b78a" : "#2b8a63",
      warningColor: theme === "dark" ? "#e2a94f" : "#b9791d",
      errorColor: theme === "dark" ? "#e06a67" : "#c54545"
    }
  } satisfies GlobalThemeOverrides;
  if (theme === "dark") {
    return {
      ...shared,
      common: {
        ...shared.common,
        bodyColor: "#141817",
        cardColor: "#1c2220",
        modalColor: "#1c2220",
        popoverColor: "#222a27"
      }
    };
  }
  return {
    ...shared,
    common: {
      ...shared.common,
      bodyColor: "#f2f5f4",
      cardColor: "#ffffff",
      modalColor: "#ffffff",
      popoverColor: "#ffffff"
    }
  };
};
