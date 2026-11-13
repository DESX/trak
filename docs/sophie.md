# Sophie

Nov 12, 2026 · Joel Chenette

Sophie is an agent harness for chat-only model APIs. The model's every reply is a bash script; Sophie checks it, runs it, and feeds the result back. All state, configuration included, lives in one trak 1.2 file.

## Backends and login

- Two backends: commercial and GovCloud (CUI-compliant, no web tools). Each has its own key ID and secret.
- Key IDs and secrets live in a small credentials file, never in the log. A login also needs the user's password and lasts one hour.
- The web UI can remember the password in the browser only, never on the server.
- Each backend has an opaque 24-hour token quota. When blocked, Sophie waits and retries if configured to.

## Sessions

- A Sophie session spans any number of API chat sessions, which are invisible to the user.
- A new session takes its backend, model, effort, working directory, prompts and limits from the defaults, each overridable.
- Many sessions can run at once. One main loop owns the log and dispatches work to threads, so file order is the true order of everything.

## The turn loop

1. The user types a message, or Sophie sends an automatic prompt. The first prompt of each API session includes the base prompt and the working directory's AGENTS.md.
2. The reply must be bash. Sophie runs `bash -n` and requires a `sophie_start <level> <summary>` call first.
3. In nanny mode the user approves, edits or rejects the script. Otherwise it runs at once in the working directory.
4. A watchdog timer starts at the chosen level (1 to 4). If it fires before the script ends, Sophie sends a watchdog turn into the same API session. That turn has a short, fixed limit; if it overruns, Sophie kills it and reports a timeout.
5. When the script ends, Sophie sends its output back as the next turn.

The script talks to the user with `sophie_chat <text>` and ends a task with `sophie_done <message>`. Bash functions also cover every user-facing action, so agents can start and inspect other sessions.

## Compaction

When token use passes the session's threshold, Sophie sends the compaction prompt. The reply, the one non-bash reply, is a handover document. Sophie opens a new API session with the same setup and sends the handover as the first user prompt. The handover holds only what was in context; anything else the new agent rediscovers with bash.

## Configuration

Configuration is part of the trak tree, not a separate file. Each setting is an event list, so a change is one small append, the current value is the latest item, and every past value stays on record. The UI and CLI edit settings by appending.

## Schema

The whole application is one trak 1.2 root. Defaults are versioned settings, so the history of every setting is on record. A session captures its setup when it's created, and points at the exact prompt versions it uses.

```
# Sophie: an agent harness for chat-only model APIs.
root sophie {
  version: str,                       # Sophie version that created the file
  defaults: settings,                 # session defaults and their history
  logins: event login,
  quota: event quota,
  sessions: event session,
}

# Session defaults. Each setting is an event list; its latest item is current.
object settings {
  backend: event enum(commercial, govcloud),
  model: event str,
  effort: event str,
  workdir: event str,                 # directory new sessions run in
  compact_at: event num,              # token count that triggers compaction
  nanny: event enum(on, off),         # approve each script before it runs
  retry_quota: event enum(on, off),   # wait out a quota block, then retry
  watchdog: {                         # limits, seconds
    w1: event num,
    w2: event num,
    w3: event num,
    w4: event num,
    check: event num,                 # hard limit on a watchdog turn's script
  },
  prompts: {
    base: event |str,                 # opens every API session
    watchdog: event |str,
    timeout: event |str,
    compaction: event |str,
  },
}

# A login attempt. Keys, secrets and passwords are never logged.
object login {
  backend: enum(commercial, govcloud),
  outcome: enum(ok, failed),
  expires: num?,                      # end of the one-hour login, ms since epoch
  error: str?,
}

# The 24-hour quota changed state.
object quota {
  backend: enum(commercial, govcloud),
  state: enum(blocked, open),
  seen_in: path?,                     # the turn that hit the block
}

# One Sophie session, spanning any number of API sessions.
object session {
  setup: setup,                       # fixed when the session is created
  spawned_by: path?,                  # the turn of the agent that started it
  name: event str,
  nanny: event enum(on, off),
  state: event enum(active, archived),
  turns: event turn,
  messages: event message,            # the chat window
  compactions: event compaction,
  results: event result,              # task-complete calls
}

# A session's setup, resolved from the defaults and the user's overrides.
object setup {
  backend: enum(commercial, govcloud),
  model: str,
  effort: str,
  workdir: str,
  compact_at: num,
  retry_quota: enum(on, off),
  watchdog: {                         # seconds
    w1: num,
    w2: num,
    w3: num,
    w4: num,
    check: num,
  },
  prompts: {                          # the exact prompt versions used
    base: path,                       # e.g. defaults.prompts.base.3
    watchdog: path,
    timeout: path,
    compaction: path,
  },
}

# Token usage of an API session.
object context {
  used: num,
  limit: num,
}

# One prompt and its reply, checked and ready to run.
object turn {
  kind: enum(user, auto, watchdog, timeout, resume),
  api_session: str,
  about: path?,                       # watchdog, timeout: the running turn; resume: the compaction
  latency: num,                       # ms from send to reply
  retries: num,                       # API retries before the reply
  prompt: |str,                       # the full text sent
  reply: |str,                        # the bash script
  context: context,                   # after the reply
  check: enum(ok, syntax, no_start),  # bash -n and the start call
  check_error: |str?,
  launch: event launch,
  exit: event exit,
}

# The script was approved or rejected, and started if approved.
object launch {
  approval: enum(auto, approved, edited, rejected),
  edited: |str?,                      # the script as the user changed it
  level: enum(w1, w2, w3, w4),
  summary: str,                       # from sophie_start
  pid: num?,
}

# The script ended.
object exit {
  reason: enum(done, killed, timeout),
  code: num?,
  stdout: |str,
  stderr: |str,
  duration: num,                      # ms
}

# A chat line, from the user or from sophie_chat.
object message {
  from: enum(user, agent),
  text: |str,
  turn: path?,                        # agent: the turn that sent it
}

# A handover to a new API session.
object compaction {
  from_session: str,
  to_session: str,
  at: context,
  prompt: |str,
  handover: |str,
}

# A sophie_done call.
object result {
  message: |str,
  turn: path,
}
```

| Event list | Appended when | Covers |
|---|---|---|
| `defaults.*` | A default changes | Configuration history |
| `logins` | A login attempt finishes | Authentication and expiry |
| `quota` | The quota flag changes | Daily blocks |
| `sessions` | A session is created | Its setup; parent of everything below |
| `sessions.N.name`, `.nanny`, `.state` | The user changes them | Live session settings |
| `sessions.N.turns` | A reply arrives and is checked | Prompt, reply, retries, tokens, checks |
| `…turns.N.launch` | The script starts, or is rejected | Approval, watchdog level, summary |
| `…turns.N.exit` | The script ends | Output, code, kills, duration |
| `sessions.N.messages` | A chat line is sent | The chat window |
| `sessions.N.compactions` | A handover is written | API session switches |
| `sessions.N.results` | The agent calls sophie_done | Task completion |

A normal turn writes three appends: turn, launch, exit. Nanny mode at a turn needs no field: it's `sessions.N.nanny` read at that turn's version.
