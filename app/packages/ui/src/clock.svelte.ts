/**
 * The clock times are shown on. Reactive, so every label that formats a time
 * redraws when the setting changes; `Main` sets it from the account's settings.
 */
export const clock = $state({ hour12: deviceHour12() });

/** What the device's language uses, for an account that hasn't chosen. */
export function deviceHour12(): boolean {
  return new Intl.DateTimeFormat(undefined, { hour: 'numeric' }).resolvedOptions().hour12 ?? false;
}
