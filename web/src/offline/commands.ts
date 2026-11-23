/**
 * Command catalogue: every offline-capable write, keyed by its wire `type`.
 * M1 adds entries such as `'inventory.consume': { itemId: string; delta: string }`.
 * Kept as an interface so feature modules can extend it via declaration merging.
 */
// biome-ignore lint/suspicious/noEmptyInterface: extended by feature modules from M1.
export interface CommandPayloads {}

export type CommandType = keyof CommandPayloads
