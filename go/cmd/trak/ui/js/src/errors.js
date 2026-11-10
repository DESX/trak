// Errors, matching the other reference libraries (docs/api.md).

/** Base class for every error this library throws. */
export class TrakError extends Error {
  constructor(message) {
    super(message);
    this.name = this.constructor.name;
  }
}

/** The bytes don't follow the format. `offset` is where the problem is. */
export class FormatError extends TrakError {
  constructor(message, offset) {
    super(offset == null ? message : `${message} (at byte ${offset})`);
    this.message0 = message;
    this.offset = offset ?? null;
  }
}

/** The file's major version isn't 1. */
export class UnsupportedVersion extends FormatError {}

/** A schema definition is invalid. `line` and `column` are 1-based when known. */
export class SchemaError extends TrakError {
  constructor(message, line = null, column = null) {
    super(line == null ? message : `${message} (line ${line}, column ${column})`);
    this.message0 = message;
    this.line = line;
    this.column = column;
  }
}

/** A bad argument, such as a cursor that isn't the start of an object. */
export class ValueError extends TrakError {}
