// Tests store Blobs in fake-indexeddb, which clones with Node's structuredClone; that can't clone
// jsdom's Blob, so those tests use Node's own. Only the one export is typed: the app never runs
// on Node.
declare module 'node:buffer' {
  export const Blob: typeof globalThis.Blob
}
