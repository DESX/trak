// Timestamps: YYYYMMDDTHHMMSS.sssZ in UTC, handled as integer milliseconds.

const MONTH_DAYS = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
const STAMP = /^[0-9]{8}T[0-9]{6}\.[0-9]{3}Z$/;

function daysFromCivil(y, m, d) {
  y -= m <= 2 ? 1 : 0;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * (m > 2 ? m - 3 : m + 9) + 2) / 5) + d - 1;
  const doe = yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy;
  return era * 146097 + doe - 719468;
}

function civilFromDays(z) {
  z += 719468;
  const era = Math.floor(z / 146097);
  const doe = z - era * 146097;
  const yoe = Math.floor((doe - Math.floor(doe / 1460) + Math.floor(doe / 36524) - Math.floor(doe / 146096)) / 365);
  const doy = doe - (365 * yoe + Math.floor(yoe / 4) - Math.floor(yoe / 100));
  const mp = Math.floor((5 * doy + 2) / 153);
  const d = doy - Math.floor((153 * mp + 2) / 5) + 1;
  const m = mp < 10 ? mp + 3 : mp - 9;
  return [yoe + era * 400 + (m <= 2 ? 1 : 0), m, d];
}

/** Parse a trak timestamp to milliseconds since the epoch, or return null if it's invalid. */
export function parseStamp(s) {
  if (!STAMP.test(s)) return null;
  const n = (a, b) => Number(s.slice(a, b));
  const [y, mo, d, h, mi, sec, ms] = [n(0, 4), n(4, 6), n(6, 8), n(9, 11), n(11, 13), n(13, 15), n(16, 19)];
  const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0);
  if (y === 0 || mo < 1 || mo > 12 || d < 1 || d > MONTH_DAYS[mo - 1] + (mo === 2 && leap ? 1 : 0)) return null;
  if (h > 23 || mi > 59 || sec > 59) return null;
  return ((daysFromCivil(y, mo, d) * 24 + h) * 60 + mi) * 60000 + sec * 1000 + ms;
}

/** Format milliseconds since the epoch as a trak timestamp. */
export function formatStamp(ms) {
  const days = Math.floor(ms / 86400000);
  const rem = ms - days * 86400000;
  const [y, m, d] = civilFromDays(days);
  const p = (v, w) => String(v).padStart(w, "0");
  return `${p(y, 4)}${p(m, 2)}${p(d, 2)}T${p(Math.floor(rem / 3600000), 2)}${p(Math.floor(rem / 60000) % 60, 2)}` +
    `${p(Math.floor(rem / 1000) % 60, 2)}.${p(rem % 1000, 3)}Z`;
}

/**
 * Milliseconds for a timestamp given as a trak timestamp, an RFC 3339 string, a Date or a
 * number of milliseconds.
 */
export function toMillis(ts) {
  if (typeof ts === "number") return ts;
  if (ts instanceof Date) return ts.getTime();
  const ms = parseStamp(ts);
  if (ms != null) return ms;
  const d = Date.parse(ts);
  if (Number.isNaN(d)) throw new TypeError(`bad timestamp ${JSON.stringify(ts)}`);
  return d;
}
