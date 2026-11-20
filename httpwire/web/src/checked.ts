import { code, decodeUpstream, type ErrorEnvelope, isObject, MAX_STRING } from "./envelope.js";

/** The answers one source gives to an open check, a keyed re-read of one item. */
export const CHECK_ANSWERS = ["same", "updated", "gone", "error"] as const;
Object.freeze(CHECK_ANSWERS);

/** One source's answer to an open check. */
export type CheckAnswer = (typeof CHECK_ANSWERS)[number];

/** Why an `updated` or `gone` answer was not stored. */
export const NOT_STORED = ["superseded", "store_unwritable"] as const;
Object.freeze(NOT_STORED);

/** One of NOT_STORED. */
export type NotStored = (typeof NOT_STORED)[number];

/**
 * One source's answer. Which members are present follows `answer`: `same` carries none of `row`,
 * `not_stored`, `code` and `upstream` and is not stored; `updated` carries `row`, and `gone` none,
 * each either stored or carrying `not_stored`; `error` carries `code`, and `upstream` when the
 * source answered, and is not stored.
 */
export interface SourceCheck<T> {
  /** The app's name for the source, unique in its Checked. */
  readonly source: string;
  /** What the source said about the item. */
  readonly answer: CheckAnswer;
  /** The item as stored when `stored`, else as the source answered it. */
  readonly row?: T;
  /** Whether the server wrote the answer into its store. */
  readonly stored: boolean;
  /** Why an `updated` or `gone` answer was not stored. */
  readonly not_stored?: NotStored;
  /** The change feed's `Feed-Position` value right after the write, present exactly when `stored`. */
  readonly position?: string;
  /** The failure's code, in the code grammar. */
  readonly code?: string;
  /** What the source said when it answered with a failure. Untrusted, as the envelope's is. */
  readonly upstream?: NonNullable<ErrorEnvelope["upstream"]>;
}

/** An open check's answer: one entry per source the item came from. */
export interface Checked<T> {
  /** One answer per source, each source named once. */
  readonly sources: readonly SourceCheck<T>[];
}

type Members = Readonly<Record<string, unknown>>;

function oneOf(list: readonly string[], v: unknown): boolean {
  return typeof v === "string" && list.includes(v);
}

// The first rule of SourceCheck's doc `s` breaks, or "".
function problem(s: Members): string {
  const has = (name: string): boolean => Object.hasOwn(s, name);
  if (typeof s["source"] !== "string" || s["source"] === "") {
    return "source is not a non-empty string";
  }
  if (!oneOf(CHECK_ANSWERS, s["answer"])) {
    return "answer is not same, updated, gone or error";
  }
  if (typeof s["stored"] !== "boolean") {
    return "stored is not a boolean";
  }
  if (has("not_stored") && !oneOf(NOT_STORED, s["not_stored"])) {
    return "not_stored is not superseded or store_unwritable";
  }
  if (has("row") && s["row"] === null) {
    return "row is null";
  }
  if (s["answer"] === "error") {
    if (code(s, "code", MAX_STRING) === undefined) {
      return "an error answer's code is not in the code grammar";
    }
    if (has("upstream") && decodeUpstream(s["upstream"]) === undefined) {
      return "an error answer's upstream breaks the error envelope's rules";
    }
    return has("row") || s["stored"] || has("not_stored")
      ? "an error answer carries a row, stored or not_stored"
      : "";
  }
  if (has("code") || has("upstream")) {
    return `a ${s["answer"] as string} answer carries a code or an upstream, which only error does`;
  }
  if (s["answer"] === "same") {
    return has("row") || s["stored"] || has("not_stored")
      ? "a same answer carries a row, stored or not_stored"
      : "";
  }
  if (has("row") !== (s["answer"] === "updated")) {
    return "an updated answer carries no row, or a gone answer carries one";
  }
  return s["stored"] === has("not_stored")
    ? `a ${s["answer"] as string} answer is not exactly one of stored and not_stored`
    : "";
}

/**
 * Answers `a` when it holds the rules the server checks before it writes an open check's answer:
 * `sources` a list, each entry following SourceCheck's rules, its `upstream` the error envelope's,
 * and each `source` named once (compared exactly). Otherwise it throws a TypeError, which a
 * generated decoder reports as a decode failure. Members the wire does not name are kept.
 */
export function checkChecked<A extends Checked<unknown>>(a: A): A {
  const o: unknown = a;
  if (!isObject(o) || !Array.isArray(o["sources"])) {
    throw new TypeError("open check answer: sources is not a list");
  }
  const seen = new Set<string>();
  for (const [i, s] of (o["sources"] as readonly unknown[]).entries()) {
    const p = isObject(s) ? problem(s) : "the entry is not an object";
    if (p !== "") {
      throw new TypeError(`open check answer: source ${i}: ${p}`);
    }
    const name = (s as Members)["source"] as string;
    if (seen.has(name)) {
      throw new TypeError(`open check answer: source ${JSON.stringify(name)} is named twice`);
    }
    seen.add(name);
  }
  return a;
}
