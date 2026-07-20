export class ThemeLoadError extends Error {
  public constructor(public readonly code: string, message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "ThemeLoadError";
  }
}
