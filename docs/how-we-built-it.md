# How we built it

The **code** is written by AI coding agents (Claude). The **decisions** are
not: one human set the goals and made them. The agents also wrote the tests
and this documentation.

It grew out of a private homelab tool. The goals: almost no CPU, little RAM,
secure by default, easy to set up, and no dependencies beyond the Go standard
library.

## Decisions the human made

- **Go.** Agents built the same program in Go, C, Rust and Zig, measured on
  one arm64 board. C and Rust used less RAM, but only Go needed no third-party
  code and has HTTP and TLS in its standard library.
- **A terminal program, no web interface**, so no port is open.
- **No network scan.** You name what to watch.
- **No apt repository.** Updates are a new `.deb` from the releases.
- **Telegram stays quiet about hints.** Help for a failing check stands only
  in the detail view, not in the alarm.
- **Old config keeps working.** New features get new keys or new options;
  `HTTP_` lines keep their old rule.
- **MIT licence.**

## How it was checked

- Separate agent reviews for performance, operation and security, and a final
  review before release. Every finding that held is fixed with a test.
- A load test ran up to 1000 checks with 10% of one core and 32 MB RAM. The
  24 hour history then does not fit: it holds a full day only up to about 25
  checks ([limits](config.md#limits)).
- The human set it up as a new user (with 0.3.0). Those findings shaped the
  releases up to 0.6.0: one address field, the history curves, the welcome
  screen.
- Releases are built reproducibly and signed after an independent rebuild
  ([SECURITY.md](../SECURITY.md)).

Statements here rest on tests or on measurements on one Radxa Cubie A7S. Not
audited by third parties.
