# How we built it

One human set the goals and made the decisions. AI coding agents (Claude) wrote
the code, the tests and the documentation.

It grew out of a private homelab tool. The goals: almost no CPU, little RAM,
secure by default, easy to set up, and no dependencies beyond the Go standard
library.

**Why Go.** Agents built the same program in Go, C, Rust and Zig, measured on
one arm64 board. C and Rust used less RAM, but only Go needed no third-party
code and has HTTP and TLS in its standard library.

**Checks.** Separate agent reviews for performance, operation and security, and
a final review before release. Every finding that held is fixed with a test.
A load test ran up to 1000 checks with 10% of one core and 32 MB RAM.

Statements here rest on tests or on measurements on one Radxa Cubie A7S. Not
audited by third parties.
