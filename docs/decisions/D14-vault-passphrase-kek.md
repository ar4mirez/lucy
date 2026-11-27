---
id: D14
title: "D14: the vault's org keys are wrapped under a passphrase-derived key"
status: accepted
version: 2
owner: ar4mirez
requirements: [LUCY-CRED-002]
decided: 2026-11-27
---

# D14: the vault's org keys are wrapped under a passphrase-derived key

## Context

The vault ([vault](../tech/vault.md), LUCY-CRED-002) seals each credential under its org's own
key, but until now those keys sat in plain files beside the sealed credentials, so anyone who
could read lucy's data directory as its user could open every credential (#32). No real
credential was to go into the vault before the org keys were wrapped with a key that does not sit
beside them. The options were an OS keyring or secret service, a passphrase-derived key, or a
hardware-backed key, each with a fallback. lucy runs headless as a service on the owner's machine,
so the choice has to work without a desktop session.

## Decision

The org keys are wrapped under a key-encryption key derived from a passphrase, which works
headless:

- **Derivation.** PBKDF2-HMAC-SHA256 from the standard library (`crypto/pbkdf2`), 600,000
  iterations for a new vault, with a 16-byte random salt. The salt, the iteration count and a check
  value sealed under the derived key are kept in `keys/kek`, beside the wrapped keys; a vault whose
  parameters name fewer than 100,000 iterations is refused. The passphrase and the derived key are
  never stored.
- **Wrapping.** Each org's 32-byte key is sealed with AES-256-GCM under the derived key, bound to
  its org, in `keys/<org>.wkey`.
- **Where the passphrase comes from.** `lucy gateway` and `lucy vault set` read it from a file
  they are pointed at (`--passphrase-file`, which must be a plain file only its owner can read,
  outside the vault and lucy's data directory; under systemd, for example, `--passphrase-file
  ${CREDENTIALS_DIRECTORY}/lucy-vault`), else they ask once on the terminal with echo off. Never
  from an argument or the environment. `lucy vault ls` and `rm` need no passphrase.
- **Migration.** The first unlock of a vault made before keys were wrapped sets the passphrase,
  wraps every plain key and removes the plain copies. Once a vault's keys are wrapped, a plain key
  is refused, never wrapped.

## Consequences

- A copy of the whole vault, keys included, opens nothing without the passphrase. Someone who can
  run code as the owner's user while the gateway is running can still read the derived key from
  its memory; that remains the host's account security (`lucy preflight --check`, m1 slice 15).
- A lost passphrase cannot be recovered: every credential has to be set again. The owner keeps the
  passphrase in a password manager.
- A headless gateway needs the passphrase at start, from a 0400 file or a service manager's
  credential; the file is as sensitive as the passphrase and lives outside lucy's data directory,
  so a backup of that directory does not carry it.
- Changing the passphrase (re-wrapping every org key) is not built yet; nor is an OS keystore or a
  hardware-backed key, which stay later options.
