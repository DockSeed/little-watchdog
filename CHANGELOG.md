# Changelog

## [Unreleased]

## [0.5.0] - 2026-11-05

### little-wd

- From 120 columns the menu has the side panel too: every FAIL, then every
  unstable or recovering check, each with since when and the last result;
  when nothing needs a look, one line "All OK, N checks.". Below it this
  machine, as in Status. A long list on a low terminal drops the machine
  part first, then ends with "more in Status".
- The "<host> right now" panel no longer has the little-wd line (RAM,
  writes/h); it is in the menu's bottom line already.
- From 160 columns the screen stays 160 wide and sits in the middle, so 120
  to 159 columns are used in full. 80×24 is unchanged.
- The side panel and the "<host> right now" panel show the last 24 hours as
  curves (▁▂▃▄▅▆▇█). For the selected service check: how often it
  answered (red where it failed) and how fast. For this machine: CPU, RAM,
  disk, temperature and waiting time, each with its peak. A curve has up to
  48 cells (30 min each), fewer where the panel is narrow. On a low terminal
  the curves are dropped first.
- The row "Stalled (PSI)" is now "waited"; the curve under it shows the
  rest. The "no PSI" notes stay.
- Enter on a check shows the same curves in its details, so 80×24 has them
  too: how often and how fast for a service check, the machine curve with
  its peak for CPU, disk, memory and temperature.

### Daemon

- New option `STATE_IN_RAM=yes` ("State in RAM" in Settings): the state file
  lives in `/run/little-watchdog` and the daemon writes nothing to disk.
  The unit gets `RuntimeDirectory=little-watchdog` (mode 0750, kept over a
  restart). Default stays on disk. Switching says in one sentence that a
  reboot loses the history and the "since" times and may send an alarm
  twice; the running daemon moves the state at the next reload. The bottom
  line of little-wd says "state in RAM, no disk writes" instead of
  counting writes. An older version skips the new key with a warning.
- Commands sent while the daemon was down are not answered after a start,
  with or without a state file (the Telegram offset was never kept in it);
  now covered by a test.
- The state file keeps a 24 hour history in 10 minute buckets: per check how
  often it answered and failed and the mean answer time, per machine mean
  and peak of CPU, RAM, disk, temperature and PSI. It is written with the
  writes that are due anyway, so the number of writes stays the same, and
  it stays under 16 KB. Pressure is read from `/proc/pressure/*` once per
  round; a machine without PSI or a sensor simply has no curve for it.
  The new field `history` is additive: 0.4.0 ignores it, and a broken or
  unknown history is dropped without stopping the daemon.
- If the state cannot be moved to the new place at a reload (STATE_IN_RAM=yes
  while the unit has no `RuntimeDirectory=`), the daemon keeps writing to the
  old place and logs a warning, instead of writing nowhere. At start it falls
  back to the disk path the same way.
- A restart with the clock behind the saved history (a board without a
  real-time clock before NTP) no longer wipes the 24 hours: nothing is
  recorded until the clock is past the saved end; after an hour behind it
  counts as a real step back and the history starts again.
- The mean of a host value keeps its weight over a restart inside the same
  10 minutes (new optional field `host_n`).
- A save from the sender no longer brings back the old state file after a
  move between disk and RAM.

### Security

- The package and install.sh no longer add the user who ran sudo to the
  group little-wd on their own. They ask on a new install; only `y` typed at a
  terminal adds the user, otherwise they show the `usermod` command. An
  upgrade of the .deb asks nothing.
- The package keeps its "turn on again after reinstall" marker as a
  directory, so root no longer creates a file through a link planted in
  `/etc/little-watchdog`.
- The heartbeat file and the move of the state file do not follow links.
- A web check no longer follows a redirect from a public server to a
  loopback, private or link-local address; it fails with a short message.
  The client without certificate check does not follow a redirect to
  another host.
- `docs/setup.md` shows the fingerprint of `allowed_signers` to compare
  before verifying a download.

## [0.4.0] - 2026-11-05

### Config

- New key `PING_<NAME>=host`: the host answers ping (ICMP echo over the
  unprivileged datagram socket, no capability; allowed by
  `net.ipv4.ping_group_range`, which systemd opens to all groups).
- New option `name="..."` for every check: the name shown in little-wd and
  in Telegram messages, any text. Without it the name is the key as before.
- New option `mode=` for `HTTP_`: `lenient` follows up to 5 redirects and
  takes any answer below 500, and a port that answers but is no website;
  `strict` follows redirects and takes only 2xx. `HTTP_` lines without
  `mode=` keep the old rule (200 to 399, no redirects).

### little-wd

- Add check is one form: Address, then an optional Name. No more choice
  between website, port and host: an IP or host name is pinged, `host:port`
  or a URL without a path is a lenient web check, a URL with a path a
  strict one. The menu line reads "Watch an address".
- The name may be any text; little-wd makes the config key from it (ä to
  ae and so on, `_2` for a name already used). An empty name is taken from
  the address.
- Enter saves the address at once, checks it once and shows the result in
  the form, then clears it for the next address. A failed check is saved too
  and the result says why. Esc leaves, also while the check runs; the check
  stays saved.
- A port that answers but not as a website ("answers, no website") is OK but
  shown yellow, in the list and in the menu summary. It sends no message.
  After a redirect this no longer applies: a redirect to a dead target fails.
- A passing web check reads "answers, 200 in 16 ms" for old `HTTP_` lines too
  (was "HTTP 200 in 16ms"); a failing one keeps "HTTP 404 in 16 ms".
- Status, Remove check and messages show the name from `name=`.
- Status groups the services by host: checks on the same IP or host name
  (port, path and scheme left out) stand under one line with the host. A host
  with only one check gets no line of its own; these checks come first, as
  before. The order depends on the config only, never on a result.
- `a` in Status opens Add check with the host of the check under the cursor
  already in the address: type `:8096` and a name. On a check of this machine
  the address is empty. Esc goes back to Status. Every service stays a check
  of its own; config, state file and Telegram messages are unchanged.
- Details of a failing check (Enter on it) show "What to do:" under the
  last result: one short hint per kind of failure (refused, blocked, not
  reachable, no answer, name not found, certificate, HTTP 4xx/5xx, text
  missing, cpu, memory, disk, temperature). It names possible causes only.
  Telegram messages stay as they are.

### State file

- New fields per check: `help` (the hint above, set while the check fails)
  and `odd` (OK, but shown yellow). Additive: older versions ignore them.

## [0.3.1] - 2026-11-05

### Config

- Check values may carry options after the URL or host:port: `key=value` or
  `key="value with blanks"`. An unknown option is a warning and is skipped,
  so a config written by a newer version still starts (0.3.0 refused it with
  "unexpected text after the URL"). TCP_ and HOST_ take options too.
- `contains=TEXT` works as before. New: `contains="TEXT"` is read without
  the quotes, so options may follow it. little-wd saves a text that starts
  with a quote in that form.

### little-wd

- Remove check: a check without a first result yet shows [?] instead of an
  empty State column (only while the daemon has written state).
- Dialogs sit in the optical centre (40 % of the free height above them)
  instead of at the top. A dialog taller than the screen starts at the top
  as before.

### Daemon

- The unit sets `GODEBUG=disablethp=1`. Where transparent huge pages are
  `always` (`/sys/kernel/mm/transparent_hugepage/enabled`), the kernel backed
  the small Go heap with 2 MB pages; the daemon used 2 to 4 MB more RAM, at
  random. Elsewhere the setting changes nothing. Measured on arm64 with 12 checks: RSS 13.7–15.3 MB before,
  11.2–11.5 MB after; memory charged to the unit 9–11 MB before, 5–5.6 MB after.

- On a board without a real-time clock the daemon start ("Started", the
  footer, "since the daemon start") showed the clock from before NTP set it.
  The daemon now notices the jump (wall clock against monotonic time, within
  the first hour) and corrects the start once.

### Checks

- A firewall that rejects a port (firewalld: icmp-host-prohibited) no longer
  reads "host not reachable": an immediate "no route to host" now says "port
  80 blocked by a firewall, or no route to the host". A host that is off
  fails only after ARP gives up (about 3 s) and still reads "host not
  reachable".

## [0.3.0] - 2026-11-04

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
