/** Bytes that don't follow the format. `offset` is where, or -1. */
export class FormatError extends Error {
  constructor(msg, offset = -1) {
    super(offset < 0 ? msg : `${msg} (at byte ${offset})`);
    this.name = "FormatError";
    this.offset = offset;
  }
}

/** A file whose version this reader doesn't read. */
export class UnsupportedVersionError extends FormatError {
  constructor(version) {
    super(`unsupported version ${version}`, 5);
    this.name = "UnsupportedVersionError";
    this.version = version;
  }
}

/** An invalid schema. `line` and `column` are 1-based, or 0. */
export class SchemaError extends Error {
  constructor(msg, line = 0, column = 0) {
    super(line ? `${msg} (line ${line}, column ${column})` : msg);
    this.name = "SchemaError";
    this.line = line;
    this.column = column;
  }
}

/** A path, version or item that doesn't exist. */
export class NotFoundError extends Error {
  constructor(msg) {
    super(msg);
    this.name = "NotFoundError";
  }
}
