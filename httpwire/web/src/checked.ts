import type { ErrorEnvelope } from "./envelope.js";

const answers = [] as unknown as readonly ["same", "updated", "gone", "error"];
const reasons = [] as unknown as readonly ["superseded", "store_unwritable"];

export const CHECK_ANSWERS: typeof answers = Object.freeze(answers);

export type CheckAnswer = (typeof answers)[number];

export const NOT_STORED: typeof reasons = Object.freeze(reasons);

export type NotStored = (typeof reasons)[number];

export interface SourceCheck<T> {
  readonly source: string;
  readonly answer: CheckAnswer;
  readonly row?: T;
  readonly stored: boolean;
  readonly not_stored?: NotStored;
  readonly code?: string;
  readonly upstream?: NonNullable<ErrorEnvelope["upstream"]>;
}

export interface Checked<T> {
  readonly sources: readonly SourceCheck<T>[];
}

export function checkChecked<A extends Checked<unknown>>(a: A): A {
  return a;
}
