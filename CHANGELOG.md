# Changelog

## [Unreleased]

### little-wd

- New menu item Settings: check interval, alarm after, all-clear after and
  timeout per check, each with a short explanation. The menu shows a summary
  ("every 1m, alarm after 3, all-clear after 2").
- Dead-man switch: the time between pings (DEADMAN_EVERY) can be set below the
  URL. System checks, temperature: an optional sensor file (TEMP_SENSOR).
- An invalid value shows the config parser's error and is not saved.
- Status, Remove check and System checks have named columns (State, Check,
  Type, ...) and one indent under each section heading.
- Status: "Since" is the clock time the state began (a date if older than
  today), for a failure the first failed check. "Checked" is the time of the
  last result. Both replace the unexplained "29 s" and "· 07:19".
- An OK from before a daemon restart counts from the restart, since nothing
  was checked while the daemon was down. Same in `little-wd status`, which
  now prints clock times instead of "29 s ago".
- Esc in the main menu asks before quitting; Q still quits at once.
- Dead-man switch: Enter on the URL moves to Every, a second Enter saves;
  saving pings the URL once, also when only Every changed.
- A value without a verdict yet (CPU warm-up, a sensor read error) is dimmed;
  `little-wd status` adds "(no verdict yet)".
- The help explains [OK], [FAIL], [2/3], [?], Checked and "back 1/2".
- Times from an earlier year show the year, also in Telegram.
- Status on a terminal of 120 columns or more gets a side panel: the check
  under the cursor (type, target, state and since, result, Telegram) and the
  machine right now (load, stalled time from PSI, uptime from /proc, read every
  5 s; little-wd's own RAM and disk writes). Below 120 columns the screen is
  unchanged.

### Telegram messages

- /status no longer shows a check without a verdict yet (CPU warm-up, a
  sensor read error) as freshly OK: "ok before the restart at 20:56, first
  value in 1 min", or "ok at 21:16, ..." after a config change. A failing check
  reads "down since 19:56, first value in 1 min".
- /status shows a failed check that answers again but is not yet back as
  "answers again, all-clear after 1 more good try. Down since 19:56."
  (RECOVER_AFTER), not the old error. `little-wd status` adds the same hint.
- The start message lists the checks in /status order: this machine first,
  then the services.

### Packaging and docs

- The .deb and the .tar.gz include the licence of the Go runtime built into
  little-wd (BSD-3-Clause) as LICENSE.go.
- The key fingerprint is in SECURITY.md, allowed_signers, docs/setup.md and
  the release notes; the link to an outside website is gone.
- README states that the project is not affiliated with Telegram or
  Healthchecks.io.
- docs/setup.md and docs/config.md describe Settings, Every and Sensor; the
  README lists the commands and the Status side panel.

## [0.2.0] - 2026-11-03

### Upgrade note

- If a dead-man switch is set up, one message "dead-man switch is active, first
  ping OK at HH:MM" arrives once after the upgrade. It is the new confirmation
  that the first ping went through, not an alarm.
- Messages still queued from before the upgrade (state file version 1) are
  converted once to the new HTML format on the first start. A rollback to
  v0.1.0 afterwards shows raw tags such as `<b>` in messages queued meanwhile.

### Telegram messages

- Formatted with Telegram HTML: check and host names in bold, config paths and
  `little-wd check` in a fixed font, bold /status header and group headings.
  All names, hosts and error texts are escaped; if Telegram ever rejects the
  formatting, the message is sent again as plain text, so no alarm is lost.
- Rewritten in plain English: what happened, where, since when, what to do.
  Alarms keep "since HH:MM" and name the limit ("limit 90%"); the clock time no longer leads every message. A
  message that arrives more than 2 minutes after its event ends with
  "(sent late, event at HH:MM)".
- The start message reads "watching 4 checks (disk /, memory, temperature, cpu).
  /status shows the details." and is retried until it is delivered.
- `/status` is grouped under "This machine" and "Services" (same order in
  `little-wd`) and shows values without "ok (...)" or double brackets.
- A config that cannot be used says which line is wrong and where to fix it
  (the real config path); the daemon starts by itself once it is fixed.
- A `getUpdates` conflict (a webhook, or another program using the same bot) is
  named once per streak: "this bot is also used elsewhere, so /status does not
  work here. Use one bot per machine." The state is shown in `little-wd`.

### little-wd

- Status: health chip in the header, unstable checks ("2/3") and recovery
  progress ("back 1/2"), value time, dead-man and Telegram state in the menu.
- New System checks screen: change limits and turn cpu, memory, disk and
  temperature on or off (an empty `DISK_PATHS` counts as off).
- Text fields have a real cursor (Left, Right, Home, End, Delete). Secrets are
  masked; Ctrl-R shows them; only a fingerprint of the current token or URL is
  shown.
- Dead-man switch and Telegram show the result of the last ping and send, with
  the time. An old send error with nothing left to send no longer stays red
  for days.
- Friendlier errors for network problems (DNS, refused, timeout, TLS) and a
  config error in red on the menu with a "config invalid" chip.
- One footprint line in the menu: memory, CPU share and disk writes per hour of
  the daemon, taken from its own state file.

### Fixes

- The CPU window shown in System checks (for example 30m or 1h30m) can be saved
  again.
- A flapping check no longer writes the state file every round: counter
  changes are written at most every 5 minutes (state or message changes at
  once). Worst case 288 writes a day for a flapping check, 144 when nothing
  happens.
- A send cut short by shutdown is not counted as a Telegram failure.
- The `.linked` and `.pairing` marker files are created without following
  symlinks and are group-writable regardless of the umask.
- Text read from the state file (detail, last message) is cleaned before it is
  shown.
- The dead-man URL field no longer says "Empty = off" (empty keeps the current
  URL; type `off` to switch it off).

## [0.1.0] - 2026-11-03

First release.
