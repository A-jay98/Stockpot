import type { components } from '../api/schema'

/** The OpenAPI `Command` union, which the server validates every batch against. */
type Command = components['schemas']['Command']

/**
 * Payload type per command `type`, generated from the OpenAPI contract
 * (`oneOf` + discriminator on `Command`), so a payload can't drift from the server.
 */
export type GeneratedPayloads = { [C in Command as C['type']]: C['payload'] }

/**
 * Command catalogue: every offline-capable write, keyed by its wire `type`.
 * Kept as an interface so tests (and feature modules) can add entries by declaration merging.
 */
// biome-ignore lint/suspicious/noEmptyInterface: extended by declaration merging.
export interface CommandPayloads extends GeneratedPayloads {}

export type CommandType = keyof CommandPayloads
