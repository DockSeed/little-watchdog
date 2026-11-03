# Changelog

## [Unreleased]

### Upgrade note

- If a dead-man switch is set up, one message "dead-man switch is active, first
  ping OK at HH:MM" arrives once after the upgrade. It is the new confirmation
  that the first ping went through, not an alarm.

### Telegram messages

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
