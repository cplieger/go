// RFC 3339 section 5.6 date-time as Go's time.Time JSON writes it: uppercase
// T, 0 to 9 fraction digits, Z or a ±hh:mm offset. Not Date.parse, which
// ECMA-262 (21.4.1.32) specifies only for exactly three fraction digits.
const DATE_TIME =
  /^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?(?:Z|([+-])([0-9]{2}):([0-9]{2}))$/;

const MINUTE = 60_000;
const DAY = 86_400_000;
const NAMED_LENGTH = 64;

function isLeap(y: number): boolean {
  return y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
}

function daysInMonth(y: number, m: number): number {
  return m === 2 ? (isLeap(y) ? 29 : 28) : [4, 6, 9, 11].includes(m) ? 30 : 31;
}

// Days from 1970-01-01 in the proleptic Gregorian calendar, the count
// http://howardhinnant.github.io/date_algorithms.html#days_from_civil derives.
// Not Date.UTC, which reads a year from 0 to 99 as 1900 plus that year.
function daysFromCivil(year: number, m: number, d: number): number {
  const y = m <= 2 ? year - 1 : year;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * ((m + 9) % 12) + 2) / 5) + d - 1;
  return era * 146_097 + yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy - 719_468;
}

function refuse(text: string): SyntaxError {
  const named =
    text.length > NAMED_LENGTH
      ? `${JSON.stringify(text.slice(0, NAMED_LENGTH))}…`
      : JSON.stringify(text);
  return new SyntaxError(`parseInstant: not an RFC 3339 date-time: ${named}`);
}

/**
 * Reads a wire instant, RFC 3339 `date-time` as a Go server writes it (uppercase `T`, 0 to 9
 * fraction digits, `Z` or a `±hh:mm` offset), as Unix milliseconds. Digits past the third are
 * truncated toward the earlier millisecond, so two instants within one millisecond compare equal.
 * Any other text throws a `SyntaxError` naming it. Compare instants through this, never as text;
 * a `<time datetime>` takes `new Date(parseInstant(text)).toISOString()`.
 */
export function parseInstant(text: string): number {
  const m = DATE_TIME.exec(text);
  if (m === null) {
    throw refuse(text);
  }
  const [year, month, day, hour, minute, second] = m.slice(1, 7).map(Number) as [
    number,
    number,
    number,
    number,
    number,
    number,
  ];
  const [, , , , , , , fraction = "", sign, offsetHour = "00", offsetMinute = "00"] = m;
  if (
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > daysInMonth(year, month) ||
    hour > 23 ||
    minute > 59 ||
    second > 59 ||
    Number(offsetHour) > 23 ||
    Number(offsetMinute) > 59
  ) {
    throw refuse(text);
  }
  const offset =
    (sign === "-" ? -1 : 1) * (Number(offsetHour) * 60 + Number(offsetMinute)) * MINUTE;
  const millis = Number(fraction.padEnd(3, "0").slice(0, 3));
  return (
    daysFromCivil(year, month, day) * DAY +
    ((hour * 60 + minute) * 60 + second) * 1000 +
    millis -
    offset
  );
}
