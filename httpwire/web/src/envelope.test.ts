import { describe, expect, it } from "vitest";
import { decodeEnvelope } from "./envelope.js";
import { ENVELOPE_FIXTURES } from "./testing/index.js";

function decodeBody(body: string): ReturnType<typeof decodeEnvelope> {
  let v: unknown;
  try {
    v = JSON.parse(body);
  } catch {
    return undefined;
  }
  return decodeEnvelope(v);
}

function fixture(name: string): string {
  const f = ENVELOPE_FIXTURES.find((x) => x.name === name);
  if (f === undefined) {
    throw new Error(`no fixture ${name}`);
  }
  return f.body;
}

const minimal = {
  code: "not_found",
  message: "No such row.",
  remediation: "Check the id.",
  request_id: "req-1",
};

describe("the golden envelope fixtures", () => {
  it("holds the writer's answers, valid bodies and refused ones", () => {
    const names = ENVELOPE_FIXTURES.map((f) => f.name);
    for (const name of [
      "catalog-upstream",
      "catalog-upstream-no-status",
      "catalog-upstream-message-long",
      "catalog-upstream-message-control",
      "server-default-not-found",
      "server-default-outside-specs",
    ]) {
      expect(names).toContain(name);
    }
    expect(ENVELOPE_FIXTURES.some((f) => f.valid)).toBe(true);
    expect(ENVELOPE_FIXTURES.some((f) => !f.valid)).toBe(true);
  });

  for (const f of ENVELOPE_FIXTURES) {
    it(`${f.name} is ${f.valid ? "decoded" : "refused"}`, () => {
      expect(decodeBody(f.body) !== undefined).toBe(f.valid);
    });
  }
});

describe("decodeEnvelope", () => {
  it("answers the catalog writer's upstream answer member by member", () => {
    expect(decodeBody(fixture("catalog-upstream"))).toStrictEqual({
      code: "arr_refused",
      message: "Sonarr refused the search.",
      remediation: "Check the Sonarr connection in Settings.",
      request_id: "req-1",
      upstream: { service: "sonarr", status: 401, message: "Unauthorized" },
    });
  });

  it("leaves status out of an upstream that gave none", () => {
    const env = decodeBody(fixture("catalog-upstream-no-status"));
    expect(env?.upstream).toStrictEqual({ service: "sonarr", message: "connection refused" });
  });

  it("answers fields, reason and retry_after_ms as sent", () => {
    expect(
      decodeEnvelope({
        ...minimal,
        reason: "row_changed",
        fields: [{ path: "retention.days", code: "range", message: "Use 1 to 400." }],
        retry_after_ms: 1500,
      }),
    ).toStrictEqual({
      ...minimal,
      reason: "row_changed",
      fields: [{ path: "retention.days", code: "range", message: "Use 1 to 400." }],
      retry_after_ms: 1500,
    });
  });

  it("drops members outside the envelope, at the top, in a field and in upstream", () => {
    expect(
      decodeEnvelope({
        ...minimal,
        extra: 1,
        fields: [{ path: "a", code: "required", message: "x", hint: "y" }],
        upstream: { service: "github", message: "x", raw: "<b>" },
      }),
    ).toStrictEqual({
      ...minimal,
      fields: [{ path: "a", code: "required", message: "x" }],
      upstream: { service: "github", message: "x" },
    });
  });

  it("reads only own members, never inherited ones", () => {
    const inherited = Object.create({ code: "not_found" }) as Record<string, unknown>;
    Object.assign(inherited, { message: "x", remediation: "", request_id: "r" });
    expect(decodeEnvelope(inherited)).toBeUndefined();
  });

  it("counts string bounds in UTF-8 bytes", () => {
    expect(decodeEnvelope({ ...minimal, message: "é".repeat(2048) })).toBeDefined();
    expect(decodeEnvelope({ ...minimal, message: "é".repeat(2048) + "a" })).toBeUndefined();
    expect(decodeEnvelope({ ...minimal, reason: "a".repeat(65) })).toBeUndefined();
    expect(
      decodeEnvelope({ ...minimal, upstream: { service: "x", message: "é".repeat(256) } }),
    ).toBeDefined();
    expect(
      decodeEnvelope({ ...minimal, upstream: { service: "x", message: "é".repeat(256) + "a" } }),
    ).toBeUndefined();
  });

  it("counts a lone surrogate as the three bytes of U+FFFD", () => {
    expect(decodeEnvelope({ ...minimal, message: "\ud800".repeat(1365) + "a" })).toBeDefined();
    expect(decodeEnvelope({ ...minimal, message: "\ud800".repeat(1366) })).toBeUndefined();
  });

  it("refuses an undefined member as a present one that breaks its rule", () => {
    expect(decodeEnvelope({ ...minimal, reason: undefined })).toBeUndefined();
    expect(decodeEnvelope({ ...minimal, upstream: undefined })).toBeUndefined();
  });

  it("refuses a value that is not an object", () => {
    for (const v of [undefined, null, "x", 1, [], [minimal]]) {
      expect(decodeEnvelope(v)).toBeUndefined();
    }
  });

  it("refuses retry_after_ms and upstream.status off their integer ranges", () => {
    expect(decodeEnvelope({ ...minimal, retry_after_ms: Infinity })).toBeUndefined();
    expect(decodeEnvelope({ ...minimal, retry_after_ms: Number.NaN })).toBeUndefined();
    expect(
      decodeEnvelope({ ...minimal, retry_after_ms: Number.MAX_SAFE_INTEGER + 2 }),
    ).toBeUndefined();
    expect(decodeEnvelope({ ...minimal, retry_after_ms: -0 })?.retry_after_ms).toBe(0);
    for (const status of [99, 600, 401.5, Infinity]) {
      expect(
        decodeEnvelope({ ...minimal, upstream: { service: "x", status, message: "" } }),
      ).toBeUndefined();
    }
  });
});
