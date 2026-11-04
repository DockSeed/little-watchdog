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
| `DISK_PATHS` | `/` | Mount points, comma separated. |
| `DISK_MIN_FREE_PCT` | `10` | Alarm below this free space. |
| `MEM_MIN_AVAIL_PCT` | `5` | Alarm below this available memory. |
| `TEMP_MAX_C` | `85` | Alarm above this temperature. |
| `TEMP_SENSOR` | hottest | A file under `/sys/class/thermal`, `/sys/class/hwmon` or `/sys/devices`. |
| `CPU_MAX_PCT` | `90` | Alarm above this average load. |
| `CPU_WINDOW` | `15m` | Window for the average, at least `INTERVAL`. |
| `STATE_FILE` | `/var/lib/little-watchdog/state.json` | State file. |
| `TELEGRAM_API_URL` | `https://api.telegram.org` | For tests. |

`0` (or empty `DISK_PATHS`) switches an own-host check off. `<NAME>` is
letters, digits and `_`. Example: `contrib/little-watchdog.env.example`.

Where in `little-wd` (it checks the values with the same limits):

- Settings: `INTERVAL`, `FAIL_AFTER`, `RECOVER_AFTER`, `TIMEOUT`.
- Dead-man switch: `DEADMAN_URL`, `DEADMAN_EVERY`.
- System checks: the limits (and on or off), `CPU_WINDOW`, `TEMP_SENSOR`.
- File only: `STATE_FILE`, `TELEGRAM_API_URL`, more disks in `DISK_PATHS`
  (Remove check drops one).

Disk writes (SD cards wear out): the daemon writes the state file about every
10 minutes when nothing happens (144 a day). A flapping check writes it every
5 minutes instead (288 a day in the worst case, 144 when steady). Every alarm
or all-clear message adds two writes. `little-wd` shows the measured rate.
