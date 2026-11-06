# Security

## Reporting

Report vulnerabilities privately with **Report a vulnerability** on the Security
tab, or by mail to dockseed@proton.me. No public issues with details, please.
Only the newest release is fixed. Hobby project: we try to answer within a week.

## What is protected

The bot token and the ping URL. The service runs as its own user in a systemd
sandbox, opens no port, starts no programs, makes no requests except checks,
Telegram and the ping, and never updates itself. Only members of the group
`little-wd` can read the config. Certificates are verified, and foreign text
is cleaned before it is shown. One exception: a lenient web check
(`mode=lenient`) on an `http://` address whose port speaks TLS asks again over
`https://` without a certificate check.
Redirects are followed only by lenient and strict web checks, at most 5.

## What is not

- Group members can read the token. Root can do anything.
- A `localhost` check proves that something listens, not what.
- Anyone in your chat can ask for `/status`.
- Telegram and the ping host see your messages and pings.
- Without a dead-man's-switch nobody notices if the machine dies.

If the token leaks, revoke it with @BotFather.

## Release signature

Releases are built reproducibly. `SHA256SUMS` is signed with
`ssh-keygen -Y sign -n file` after an independent rebuild gave the same hashes.
The key is listed in `allowed_signers`, fingerprint
`SHA256:CMUY2ymSviJh+qEqswCASwZfhcosZc9kMfzcWAjuHEs`; the release notes name it
too. If it is lost or leaks, we announce a new key here and in the release notes.
