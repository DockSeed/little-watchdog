# Configuration

`/etc/little-watchdog/little-watchdog.env`, written by `little-wd`. To edit by
hand: one `KEY=value` per line, `#` only at the line start. Changes apply within
one `INTERVAL`, or at once with `sudo systemctl reload little-watchdog`.

| Key | Default | Meaning |
|---|---|---|
| `TELEGRAM_TOKEN` | required | Bot token. |
| `TELEGRAM_CHAT_ID` | required | Chat for messages and `/status`; negative for groups. |
| `DEADMAN_URL` | off | Healthchecks ping URL, `https://`. |
| `DEADMAN_EVERY` | `2m` | Time between pings, at least `60s`. |
| `INTERVAL` | `60s` | Time between rounds, at least `10s`. |
| `FAIL_AFTER` | `3` | Failed rounds before an alarm; whole number, at least `1`. |
| `RECOVER_AFTER` | `2` | Good rounds before the all-clear; whole number, at least `1`. |
| `TIMEOUT` | `10s` | Longest wait per check, above `0`, up to `10m`. |
| `HTTP_<NAME>` | | `url`, or `url contains=TEXT`. OK on 200 to 399. |
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
| `STATE_FILE` | `/var/lib/little-watchdog/state.json` | State file. |
| `STATE_IN_RAM` | `no` | `yes` keeps the state file in RAM (`/run/little-watchdog/state.json`, the unit's `RuntimeDirectory=`) and `STATE_FILE` is ignored. No disk writes. |
| `TELEGRAM_API_URL` | `https://api.telegram.org` | For tests. |

`0` (or empty `DISK_PATHS`) switches an own-host check off. `<NAME>` is
letters, digits and `_`. Example: `contrib/little-watchdog.env.example`.

Options after the URL or `host:port`: `key=value` or `key="value with
blanks"`, separated by blanks; inside quotes `\"` is a quote and `\\` a
backslash. Keys are lower case. An unknown key is logged as a warning and
skipped, so a line written by a newer version still starts. `contains=TEXT`
without quotes takes the rest of the line, so put it last or quote it.

| Option | For | Meaning |
|---|---|---|
| `name="Immich Fotos"` | all checks | Name shown in `little-wd` and in Telegram messages; any text. Without it the name is `<NAME>` in lower case. |
| `mode=lenient` | `HTTP_` | Follows up to 5 redirects; any answer below 500 is OK, and a port that answers but is no website ("port open, no website"). An `http://` address on a port that speaks TLS is asked over `https://`, without a certificate check. |
| `mode=strict` | `HTTP_` | Follows up to 5 redirects; only 200 to 299 is OK. |

Without `mode=` an `HTTP_` line keeps the old rule: 200 to 399, no redirects
followed. Example lines as `little-wd` writes them:

```
PING_NAS=192.168.1.10 name="NAS"
HTTP_IMMICH_FOTOS=http://192.168.1.10:2283 name="Immich Fotos" mode=lenient
HTTP_DOCKSEED_DE_HEALTH=https://dockseed.de/health name="dockseed.de/health" mode=strict
```

Ping needs no capability: little-wd uses the unprivileged ICMP socket, which
Linux allows to the groups in `net.ipv4.ping_group_range` (systemd sets
`0 2147483647`, all groups). With `1 0` (nobody) the check says so. Versions
before 0.4.0 skip `PING_` lines with a warning.

Where in `little-wd` (it checks the values with the same limits):

- Settings: `INTERVAL`, `FAIL_AFTER`, `RECOVER_AFTER`, `TIMEOUT`, `STATE_IN_RAM`.
- Dead-man switch: `DEADMAN_URL`, `DEADMAN_EVERY`.
- System checks: the limits (and on or off), `CPU_WINDOW`, `TEMP_SENSOR`.
- File only: `STATE_FILE`, `TELEGRAM_API_URL`, more disks in `DISK_PATHS`
  (Remove check drops one).

Disk writes (SD cards wear out): the daemon writes the state file about every
10 minutes when nothing happens (144 a day). A flapping check writes it every
5 minutes instead (288 a day in the worst case, 144 when steady). Every alarm
or all-clear message adds two writes. `little-wd` shows the measured rate.
The 24 hour history for the curves lives in the same file (field `history`, at
most about 16 KB) and is written with those writes, never on its own.
The last 30 minutes of every measurement (detail view: curve and what came before a
FAIL) are in a second file, `state.json.fine`, next to the state file. It is written in
the same write, replaced the same way, and adds about 170 bytes per check at a 60 s
round (at most 360 measurements, about 1.8 KB, per check). There is no extra write round,
but each round writes two files; the "writes/h" in the menu counts rounds.

With `STATE_IN_RAM=yes` (Settings, "State in RAM") the daemon writes nothing
to disk: the state file lives in `/run/little-watchdog`, which the unit
creates with `RuntimeDirectory=` (owner `little-wd`, mode 0750, so the group
`little-wd` reads it). A restart of the service keeps it. A reboot or a stop
loses the history, the "since" times and what was already announced, so an
alarm that was sent before may be sent once more. The bottom line of
`little-wd` then reads "state in RAM, no disk writes". Switching while the
daemon runs moves the state at the next reload and removes the old copy. A
unit file from before this version has no `RuntimeDirectory=`: update the
package or the unit first, otherwise the state cannot be written. An older
little-watchdog skips `STATE_IN_RAM` with a warning and keeps its state on
disk.
