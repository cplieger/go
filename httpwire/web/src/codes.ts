/**
 * Every code httpwire's Go catalog holds at its start, at the httpserver/v2 version this release
 * requires: httpwire's own and httpserver's.
 */
export const WIRE_CODES = [
  "accounts_unavailable",
  "bad_body",
  "bad_header",
  "bad_query",
  "bad_request",
  "body_too_large",
  "cross_origin_refused",
  "cursor_invalid",
  "held_limit",
  "host_not_allowed",
  "idempotency_answer_too_large",
  "idempotency_in_flight",
  "idempotency_key_invalid",
  "idempotency_key_required",
  "idempotency_key_reused",
  "idempotency_key_unsupported",
  "idempotency_store_full",
  "if_match_invalid",
  "if_match_required",
  "internal_error",
  "loopback_only",
  "maintenance",
  "method_not_allowed",
  "not_found",
  "outcome_unknown",
  "person_required",
  "precondition_failed",
  "rate_limited",
  "stale_version",
  "store_unavailable",
  "timeout",
  "too_many_auth_failures",
  "ui_unbuilt",
  "unsupported_media_type",
  "upgrade_required",
  "wire_mismatch",
] as const;
Object.freeze(WIRE_CODES);

/** A library code. An app's own codes come from its generated client's unions. */
export type WireCode = (typeof WIRE_CODES)[number];
