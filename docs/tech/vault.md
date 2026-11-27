---
id: tech-vault
title: The per-org credential vault
status: active
version: 4
owner: ar4mirez
requirements: [LUCY-CRED-002]
---

# The per-org credential vault

Where an org's credentials live, encrypted at rest with a key per org (TECH.md section 6). The
code is `internal/vault`; `lucy vault` manages it and `lucy gateway` reads it. Key handling, for
the threat model: [SECURITY.md](../../SECURITY.md).

- **Layout.** One directory, `vault/` in lucy's data directory by default, 0700, that must be a
  real directory and not a symlink: `keys/kek` holds the key-encryption parameters, `keys/<org>.wkey`
  each org's wrapped 32-byte key (0600), and `orgs/<org>/<name>.sealed` each credential (0600). Org and credential names are plain labels
  (1-63 bytes of `a-z`, `0-9`, `-`).
- **Sealing.** A credential is AES-256-GCM under its org's key, with a fresh random 96-bit nonce
  per write and `lucy-vault-v1`, the org and the name as additional data: the file is `LV1`, the
  nonce, then the ciphertext and tag. A sealed file copied to another name, or into another org's
  directory, does not open; nor does one with a single changed bit; nor does anything without its
  org's key. A credential is at most 16 KiB.
- **Keys.** An org's key is made from the system's random source on its first credential, sealed
  under the key-encryption key, written whole to a synced temp file, published with a hard link
  that never replaces a key already there, and never rewritten; a wrapped key that does not open,
  is not a plain file or is readable by anyone else is refused. Keys sit apart from the sealed
  credentials, so a copy of `orgs/` alone reveals nothing; lucy's backups (m2) leave `keys/` out.
  Deleting an org's key makes all of its credentials unrecoverable at once, which offboarding will
  use (LUCY-CRED-006, m2). A crash while a key is being published can leave
  `keys/<org>.wkey.tmp-<hex>`, a second copy of the wrapped key, so erasing an org deletes
  `keys/<org>.wkey` and every `keys/<org>.wkey.tmp-*` beside it.
- **Wrapping** ([D14](../decisions/D14-vault-passphrase-kek.md)). The key-encryption key is
  derived from the vault passphrase with PBKDF2-HMAC-SHA256 (`crypto/pbkdf2`): 600,000 iterations
  for a new vault, a 16-byte random salt. `keys/kek` is `LVK1`, the iteration count, the salt and a
  check value sealed under the derived key, so a wrong passphrase is told apart from a damaged
  vault; parameters naming fewer than 100,000 iterations are refused. A wrapped key is `LVW1`, a
  12-byte nonce and the AES-256-GCM sealed key, with `lucy-vault-key-v1` and the org as
  additional data. Until it is unlocked a vault lists names but reads and writes no credential.
- **Migration.** The first unlock of a vault from before keys were wrapped, under `keys/.lock`,
  wraps every plain `keys/<org>.key`, then publishes `keys/kek`, then removes each plain key once
  its wrapped copy opens to the same key, and any plain `keys/<org>.key.tmp-*`; a crash at any step
  leaves a vault the next unlock finishes. Once `keys/kek` exists a plain key is refused and never
  wrapped.
- **The passphrase.** `lucy gateway` and `lucy vault set` read it from `--passphrase-file` (a plain
  file, not a symlink, only its owner, this user or root, can read, outside the vault and lucy's
  data directory; one trailing newline is dropped), else ask once on the terminal (`/dev/tty`, so
  it works with stdin piped) with echo off, twice when it sets the passphrase, putting echo back
  before exiting on an interrupt. Never an argument or the
  environment. It is 12 to 1024 bytes; the caller clears it after the unlock.
- **Confinement.** Every file operation goes through an `os.Root` on the vault directory, so no
  name, path or symlink reaches outside it; a symlink in a key's place, or a symlinked org
  directory, is refused. Writes go to a new 0600 file that is synced and renamed into place, so a
  reader sees the old credential or the new one. `rm` deletes exactly one sealed file inside the
  vault.
- **Use.** Only an org's gateway reads its credentials: `lucy gateway --org <org>` opens the vault
  and asks it for that org's credentials and no other's. A credential is returned in a fresh
  buffer, which the gateway clears when the request ends. The vault never logs or prints a value;
  `lucy vault ls` shows names and when each was set.
- **Input.** `lucy vault set --org <org> <name>` reads the value from stdin, never an argument or
  the environment, so it is in no shell history or process list, and refuses a terminal, where it
  would show as it is typed; pipe it in. One trailing newline is dropped.
- **Audit.** `lucy vault set` and `rm` record each change through the daemon (`vault.record`,
  scope `admin`) as `vault.changed` on the org's `vault/<org>` stream: once before the vault is
  touched (`requested`) and once after (`applied`, or `failed` with a fixed reason for the kind of
  error: no such credential, does not open, insecure, invalid, locked or a wrong passphrase, or the
  vault operation failed), by
  name and operation only, never a value, a path or the error's text, which goes to stderr. With
  no daemon to record the request, the change is refused and the vault is left as it was. `set`
  unlocks the vault inside the recorded change, so the first unlock, which sets the passphrase and
  wraps the keys, is recorded too. `ls` is a read and records nothing.

## Not built yet

- TODO(LUCY-CRED-006): rotation, and offboarding that deletes an org's key and every leftover
  `keys/<org>.key.tmp-*` copy of it (m2).
- TODO(LUCY-CRED-002): changing the vault passphrase (re-wrapping every org key under a new one);
  an OS keystore or a hardware-backed key stays a later option (D14).
