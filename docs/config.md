# Configuration reference

`little-wd` writes the config for you. This page is for hand editing and for
the details. New here? Start with the [setup guide](setup.md).

## The file

`/etc/little-watchdog/little-watchdog.env`:

- One `KEY=value` per line. `#` only at the start of a line.
- No quoting, no shell: the file is never run.
- Changes apply within one `INTERVAL`, or at once with
  `sudo systemctl reload little-watchdog`.
- Example: `contrib/little-watchdog.env.example`.

## Keys

| Key | Default | Meaning |
|---|---|---|
| `TELEGRAM_TOKEN` | required | Bot token. |
| `TELEGRAM_CHAT_ID` | required | Chat for messages and `/status`; negative for groups. |
| `DEADMAN_URL` | off | Healthchecks ping URL, `https://` (`http://` only to localhost). |
| `DEADMAN_EVERY` | `2m` | Time between pings, at least `60s`. |
| `INTERVAL` | `60s` | Time between rounds, at least `10s`. |
| `FAIL_AFTER` | `3` | Failed rounds before an alarm; whole number, at least `1`. |
| `RECOVER_AFTER` | `2` | Good rounds before the all-clear; whole number, at least `1`. |
| `TIMEOUT` | `10s` | Longest wait per check, above `0`, up to `10m`. |
| `HTTP_<NAME>` | | `url`, or `url contains=TEXT`. OK on 200 to 399. Options below. |
| `TCP_<NAME>` | | `host:port` accepts a connection. |
| `HOST_<NAME>` | | `host:port` answers (refused counts as alive). |
| `PING_<NAME>` | | IP address or host name answers ping (ICMP echo). |
| `DISK_PATHS` | `/` | Mount points, comma separated. |
| `DISK_MIN_FREE_PCT` | `10` | Alarm below this free space. |
| `MEM_MIN_AVAIL_PCT` | `5` | Alarm below this available memory. |
| `TEMP_MAX_C` | `85` | Alarm above this temperature. |
| `TEMP_SENSOR` | hottest | A file under `/sys/class/thermal`, `/sys/class/hwmon` or `/sys/devices`. |
| `CPU_MAX_PCT` | `90` | Alarm above this average load. |
| `CPU_WINDOW` | `15m` | Window for the average, at least `INTERVAL`. |
| `STATE_FILE` | `/var/lib/little-watchdog/state.json` | State file, absolute path. |
| `STATE_IN_RAM` | `no` | `yes` keeps the state in RAM; `STATE_FILE` is then ignored. See below. |
| `TELEGRAM_API_URL` | `https://api.telegram.org` | For tests. |

- `<NAME>` is letters, digits and `_`, unique across all check types.
- `0` (or an empty `DISK_PATHS`) switches an own-host check off.
- `DISK_PATHS` takes mount points only: a plain directory reports "not mounted".
- An unknown key is logged as a warning and skipped.

## Check options

After the URL or `host:port` come options: `key=value` or
`key="value with blanks"`, separated by blanks.

- Inside quotes, `\"` is a quote and `\\` a backslash.
- Keys are lower case.
- An unknown option is logged as a warning and skipped, so a line written by a
  newer version still starts.
- `contains=TEXT` without quotes takes the rest of the line: put it last or
  quote it.

| Option | For | Meaning |
|---|---|---|
| `name="Photo library"` | all checks | Name shown in `little-wd` and in Telegram; any text. Without it the name is `<NAME>` in lower case. |
| `mode=lenient` | `HTTP_` | Follows up to 5 redirects. Any answer below 500 is OK, and so is a port that answers but is no website ("port open, no website"). An `http://` address on a port that speaks TLS is asked over `https://`, without a certificate check. |
| `mode=strict` | `HTTP_` | Follows up to 5 redirects; only 200 to 299 is OK. |
| `contains=TEXT` | `HTTP_` | The page must contain TEXT. |

Without `mode=` an `HTTP_` line keeps the old rule: 200 to 399, no redirects
followed. Example lines as `little-wd` writes them:

```
PING_NAS=192.168.1.10 name="NAS"
HTTP_PHOTO_LIBRARY=http://192.168.1.10:2283 name="Photo library" mode=lenient
HTTP_EXAMPLE_ORG_HEALTH=https://example.org/health name="example.org/health" mode=strict
```

**Ping** needs no capability. little-wd uses the unprivileged ICMP socket,
which Linux allows to the groups in `net.ipv4.ping_group_range` (systemd sets
`0 2147483647`, all groups). With `1 0` (nobody) the check says so. Versions
before 0.4.0 skip `PING_` lines with a warning.

## Where each key is in little-wd

`little-wd` checks the values with the same limits.

| Screen | Keys |
|---|---|
| Settings | `INTERVAL`, `FAIL_AFTER`, `RECOVER_AFTER`, `TIMEOUT`, `STATE_IN_RAM` |
| Telegram | `TELEGRAM_TOKEN`, `TELEGRAM_CHAT_ID` |
| Dead-man switch | `DEADMAN_URL`, `DEADMAN_EVERY` |
| System checks | the limits (and on or off), `CPU_WINDOW`, `TEMP_SENSOR` |
| Add check, Remove check | `HTTP_`, `PING_`; Remove also drops `TCP_`, `HOST_` and a disk |
| File only | `STATE_FILE`, `TELEGRAM_API_URL`, `TCP_`, `HOST_`, more disks in `DISK_PATHS` |

## Disk writes

SD cards wear out, so the daemon writes little to disk. Only the state file
`state.json` goes to disk:

| Situation | Writes of `state.json` |
|---|---|
| Nothing happens | about every 10 minutes (144 a day) |
| A check flaps | every 5 minutes (288 a day at worst, 144 when steady) |
| Each alarm or all-clear | 2 more |

`little-wd` shows the measured rate in the menu's bottom line.

- The 24 hour history for the curves lives in the state file (field
  `history`, at most about 16 KB) and is written with those writes, never on
  its own.
- The heartbeat (`state.json.alive`, every round) and the last 30 minutes of
  every measurement (`state.json.fine`) live in the runtime directory
  `/run/little-watchdog`, which is RAM. The fine history is written in the
  same rounds as the state file, without fsync: about 170 bytes per check at a
  60 s round, at most 360 measurements (about 1.8 KB) per check. The detail
  view draws its curve and "what came before a FAIL" from it.
- The runtime directory comes from the unit (`RuntimeDirectory=`, exported as
  `RUNTIME_DIRECTORY`). Without it, for example `little-wd run` started by
  hand, both files stay next to the state file, on disk.

## State in RAM

`STATE_IN_RAM=yes` (Settings, "State in RAM") stops every disk write.

- The state file lives in `/run/little-watchdog/state.json`. The unit creates
  the directory with `RuntimeDirectory=` (owner `little-wd`, mode 0750, so the
  group `little-wd` reads it).
- It needs `RUNTIME_DIRECTORY` from the unit. Without it the state stays on
  disk and the daemon logs a warning.
- A restart of the service keeps it.
- A reboot or a stop loses the history, the "since" times and what was already
  announced. An alarm that was sent before may be sent once more.
- The bottom line of `little-wd` then reads "state in RAM, no disk writes".
- Switching while the daemon runs moves the state at the next reload and
  removes the old copy.
- A unit file from before this option has no `RuntimeDirectory=`: update the
  package or the unit first, otherwise the state cannot be written.
- An older little-watchdog skips `STATE_IN_RAM` with a warning and keeps its
  state on disk.

## Service, user and rights

| What | Where | Owner, mode |
|---|---|---|
| Program | `/usr/bin/little-wd` (`.deb`), `/usr/local/bin/little-wd` (tar.gz) | |
| Service | `little-watchdog.service`, runs `little-wd run` | user and group `little-wd` |
| Config directory | `/etc/little-watchdog` | `root:little-wd`, 2770 (setgid: new files get the group) |
| Config file | `little-watchdog.env` | `root:little-wd`, 0660 |
| State | `/var/lib/little-watchdog` (`StateDirectory=`) | `little-wd`, 0750 |
| State in RAM | `/run/little-watchdog` (`RuntimeDirectory=`) | `little-wd`, 0750 |

- The config holds the bot token. The daemon log, `little-wd status` and
  `little-wd check` warn when others, or a group other than `little-wd`, can
  read it.
- Members of the group `little-wd` read the token and change the checks. To
  add a user later: `sudo usermod -aG little-wd USER`, then log in again.
- Without the group, `little-wd` opens read-only ("explore mode, no changes")
  and says which command fixes it.
- The service opens no port and starts no programs. The unit sandboxes it
  (`ProtectSystem=strict`, no capabilities, `SocketBindDeny=any`,
  `RestrictAddressFamilies=AF_INET AF_INET6`, and more) and caps it at
  `MemoryMax=64M`. Details: `contrib/little-watchdog.service`, [SECURITY.md](../SECURITY.md).
- `sudo systemctl reload little-watchdog` re-reads the config at once.
- The service restarts 10 s after a crash. After 5 crashes in 10 minutes systemd
  gives up and the service shows as failed (`systemctl --failed`); start it again
  with `sudo systemctl restart little-watchdog`. The dead-man's switch reports the
  silence.

## Limits

| What | Limit |
|---|---|
| Checks at the same time | 24; more run in waves |
| Load test | 1000 checks on a small arm64 board, with 10% of one core and 32 MB RAM |
| 24 h history | full 24 hours only up to about 25 checks; measured: 40 checks 15 h, 100 checks 5.5 h, 200 checks 2.5 h, 400 checks 0.8 h, 1000 checks none |
| Redirects followed | 5 (lenient and strict web checks) |
| Fine history | 30 minutes, at most 360 measurements per check, in RAM and `state.json.fine` |
| 24 h history size | 10-minute sections, at most about 16 KB; with more checks the oldest sections go first |
| Telegram | at most 1 message per second to a chat, 1 every 3 seconds to a group; `/status` answered at most every 10 s |

A web check whose address is public, with or without `mode=`, may not be led
to an internal address (loopback, private, link-local), neither by a redirect
nor by a DNS answer that changes: "a public address led to an internal one,
refused".

Text from other machines (results, error messages) is cleaned before it is
shown or sent: control characters and characters that reorder or hide text
(bidi marks, zero-width characters) become blanks.
