import { localDateString } from '@runer/client-core';

/**
 * Today's date (`YYYY-MM-DD`) that every label and date picker shows.
 * Reactive, so they redraw when it changes; `Main` sets it from the core's
 * `date`, which moves on at midnight and after a wake.
 */
export const day = $state({ today: localDateString(new Date()) });
