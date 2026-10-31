# Account Recovery and Local Passwords

> **Status:** per-device vault recovery, local password reset, and password
> change are implemented for self-hosted nodes. Official-hosted account
> recovery, email login recovery, and the static browser reset page are still
> design-only.

Toget separates two questions that are easy to confuse:

| Question | Answer |
| --- | --- |
| Can I sign in as this account? | The account password verifier on this device |
| Can I decrypt old local history? | This device's vault master key, wrapped by the password or a recovery key |

Changing the password verifier without the vault key would leave the old
history unreadable. Resetting the vault key without the verifier would let the
old password keep signing in. The implementation updates both and rolls the
verifier back if the vault rewrite fails.

## The local vault

- Each device has its own random 32-byte vault master key (MK).
- MK seals account and device private keys and other local secrets with
  XChaCha20-Poly1305 and per-value AAD.
- On disk, MK is wrapped by a KEK derived from the account password with
  Argon2id (`toget-vault:v2:`). The wrapped key file is mode `0600`.
- A legacy plaintext key file is accepted and wrapped on the next successful
  login.
- The password verifier lives in the local `accounts` table. The vault is
  per-device, so changing the password on one device does not change it on
  another.

The vault protects data at rest. While the daemon is unlocked, MK is in
process memory; an attacker with control of the running process or the OS user
can read it. Full-disk encryption and locking the node are still required.

## Recovery credential

A recovery credential is an optional per-device file that wraps MK with a
separate high-entropy recovery key (RK). It is created while the vault is
unlocked:

```bash
toget account recovery create --out ~/offline/toget-recovery.togetkey
```

The suggested filename is:

```text
toget-recovery-48392017174628-01a2b3c4.togetkey
```

The first 14 digits are the account ID (displayed as `48392017-174628`);
the next segment is the first eight device ID characters.

The file is JSON:

```json
{
  "version": 1,
  "kind": "toget-device-recovery",
  "account_id": "48392017174628",
  "device_id": "0192f5a1-...",
  "server_id": "toget-server:...",
  "server_fingerprint": "ed25519:...",
  "created_at": "2026-10-31T12:00:00Z",
  "key": "<base64url, 32 bytes>",
  "checksum": "<hex SHA-256>"
}
```

- The checksum covers the format version, account, device, server,
  fingerprint, creation time, and raw key.
- RK is wrapped into the same `vault.key` envelope as the password wrap, with
  AAD bound to the account ID and device ID.
- Creating a new credential replaces the old wrap; the previous file stops
  working immediately.
- Removing it requires an unlocked vault.
- Treat the file like a private key. It is a bearer secret: anyone who has it
  and local access to this node can reset the password and open sealed history.

Only one recovery credential is active per device. Recreate it after
re-enrolling the device or changing the server identity.

## Reset a forgotten password while locked

Use the recovery file:

```bash
toget account reset-password --recovery ~/offline/toget-recovery.togetkey
```

or choose **Forgot password? Use a recovery file** on the local lock screen.

The daemon:

1. validates the file size, JSON shape, version, checksum, and 32-byte key;
2. matches account ID, device ID, server ID, and server fingerprint against
   the local account;
3. checks that this device has a recovery wrap;
4. updates the password verifier and re-wraps MK under the new password;
5. unlocks the local session.

The old password stops working, sealed history stays readable, and the
recovery credential remains valid. If the vault rewrite fails, the previous
password verifier is restored. Other devices are unchanged because they have
their own vault and verifier.

A credential for another account, device, or server is rejected. A malformed
file is rejected before any password state changes. Repeated failed attempts
are rate-limited.

## Reset while unlocked (local administrator)

If the password is forgotten but this device is still unlocked, the unlocked
daemon already holds MK and can re-wrap it directly:

```bash
toget account reset-password --local
```

The local web UI offers the same path under **Settings → Password and
recovery → Forgot the current password?**. It requires no old password and no
recovery file, and it preserves the recovery wrap. This is the self-hosted
"local administrator" reset: access to the unlocked process is already enough
to read every sealed value.

The path is deliberately unavailable while locked. `--local` and `--recovery`
are mutually exclusive.

## Change the password normally

```bash
toget account change-password
```

The current password is required. The daemon updates the verifier and re-wraps
MK under the new password, preserving the recovery wrap. The local session is
unlocked on success.

## Remove the recovery credential

```bash
toget account recovery remove
```

This requires an unlocked vault. The password still works, but a future
forgotten-password reset while locked will no longer be possible on this
device.

## Local API

All password and recovery responses use `Cache-Control: no-store` and
`Pragma: no-cache`.

| Method and path | Auth state | Purpose |
| --- | --- | --- |
| `GET /api/v1/account/recovery` | session token | Recovery status |
| `POST /api/v1/account/recovery` | unlocked | Create or replace the credential |
| `DELETE /api/v1/account/recovery` | unlocked | Remove the credential |
| `POST /api/v1/account/change-password` | session token | Current password required |
| `POST /api/v1/account/reset-password` | session token | Recovery file while locked, or `local: true` while unlocked |

The reset request is either:

```json
{ "credential": "<file contents>", "new_password": "..." }
```

or:

```json
{ "local": true, "new_password": "..." }
```

The reset endpoint stays available while the node is locked so it can accept a
recovery reset. The `local` branch checks the unlocked session itself and
returns `423 locked` otherwise. Password endpoints are serialized so the
verifier and vault wrap cannot be changed concurrently.

## What recovery cannot do

- **No credential and a locked forgotten password:** the sealed local history
  cannot be opened. This is the cost of not giving the server or the OS user a
  bypass. While the node is locked, OS root can delete the installation but
  cannot decrypt the sealed files without the password or recovery key.
- **Lost machine:** restore a tested backup and use the password or recovery
  file. If neither is available, the history is lost. Toget says this plainly.
- **Other devices:** a reset or password change on one device does not change
  another device's password or vault. There is no account-wide password yet.
- **Lost or replaced credential:** create a new one while unlocked; the old
  file is void. A lost credential cannot be regenerated from the server.
- **Server identity change:** the fingerprint and server ID in the file must
  match; a file from the old identity is rejected.
- **Power loss during a password change:** the SQLite verifier and the vault
  key file are separate writes. Ordinary write failures roll the verifier back,
  but a crash in the narrow window between them can leave the two out of step.
  A recovery file can reset access; without one, use a tested backup. The
  daemon never bypasses either check.

## Official-hosted accounts (design)

The design for official-hosted accounts remains:

- registration generates the account master key and forces a recovery
  credential download before completion;
- email resets the login credential, but does not decrypt old history;
- the static reset page reads the file locally with the File API and WebCrypto
  and never uploads RK or MK;
- the official service stores ciphertext only.

None of that hosted flow is implemented in this tree. The implemented recovery
credential is per device and targets the local vault.

Social recovery (Shamir shares) and multi-device approval are future work.
