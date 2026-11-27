---
id: tech-vault
title: The per-org credential vault
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-CRED-002]
---

# The per-org credential vault

Where an org's credentials live, encrypted at rest with a key per org (TECH.md section 6). The
code is `internal/vault`; `lucy vault` manages it and `lucy gateway` reads it. Key handling, for
the threat model: [SECURITY.md](../../SECURITY.md).

- **Layout.** One directory, `vault/` in lucy's data directory by default, 0700, that must be a
  real directory and not a symlink: `keys/<org>.key` holds each org's 32-byte key (0600), and
  `orgs/<org>/<name>.sealed` each credential (0600). Org and credential names are plain labels
  (1-63 bytes of `a-z`, `0-9`, `-`).
- **Sealing.** A credential is AES-256-GCM under its org's key, with a fresh random 96-bit nonce
  per write and `lucy-vault-v1`, the org and the name as additional data: the file is `LV1`, the
  nonce, then the ciphertext and tag. A sealed file copied to another name, or into another org's
  directory, does not open; nor does one with a single changed bit; nor does anything without its
  org's key. A credential is at most 16 KiB.
- **Keys.** An org's key is made from the system's random source on its first credential, written
  whole to a synced temp file, published with a hard link that never replaces a key already there,
  and never rewritten; a key file that is not exactly 32 bytes, not a plain file
  or readable by anyone else is refused. Keys sit apart from the sealed credentials, so a copy of
  `orgs/` alone reveals nothing; lucy's backups (m2) leave `keys/` out. Deleting an org's key makes
  all of its credentials unrecoverable at once, which offboarding will use (LUCY-CRED-006, m2).
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

## Not built yet

- TODO(LUCY-CRED-002): vault changes (set, remove) recorded in the audit chain, by name only.
- TODO(LUCY-CRED-006): rotation, and offboarding that deletes an org's key (m2).
- TODO(LUCY-CRED-002): keeping the keys in an OS keystore instead of a file, if the owner wants it;
  that needs a dependency and a decision.
