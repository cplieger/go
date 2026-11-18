import { describe, expect, it } from "vitest";
import wireCodesFile from "../testdata/wire-codes.json?raw";
import { WIRE_CODES, type WireCode } from "./codes.js";

describe("WireCode", () => {
  it("lists every code of the Go catalog's start", () => {
    // Go's httpwire test writes the file from NewCatalog, so a library code added
    // on one side fails both in one change.
    expect([...WIRE_CODES].sort()).toEqual(JSON.parse(wireCodesFile) as string[]);
  });

  it("is the union of those codes", () => {
    const codes: readonly WireCode[] = ["internal_error", "not_found", "host_not_allowed"];
    // @ts-expect-error an app's own code is no library code
    const own: WireCode = "seadex_unreachable";
    expect([...codes, own].every((c) => typeof c === "string")).toBe(true);
  });
});
