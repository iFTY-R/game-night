import axe from "axe-core";

export const assertAccessible = async (root: Element = document.body): Promise<void> => {
  const result = await axe.run(root);
  if (result.violations.length > 0) {
    const summary = result.violations.map((violation) => `${violation.id}: ${violation.help}`).join("; ");
    throw new Error(`Accessibility violations: ${summary}`);
  }
};
