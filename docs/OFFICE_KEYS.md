# Office identity and key rotation

The Back Office creates its own keys at install (#35). They never leave the
Back Office. See ARCHITECTURE.md ("Office identity") and SECURITY.md ("What
we protect").

## What `wehelpd init` creates

All files are in `office_dir`: by default `~/.wehelp/office`, and
`/var/lib/wehelp/office` on the `officekeys` volume on the appliance. The
directory is mode 700 and private keys are mode 600. `init` creates only what
is missing and never replaces a key, so running it again is safe. A key file
that group or others can read is refused at load.

| File | What it is | Who relies on it |
|---|---|---|
| `ssh_ed25519.key` / `.pub` | Office SSH key (ed25519) | The Registry lists the public key; the Relay accepts only listed keys (#4) |
| `tls.key` / `tls.crt` | Office TLS key (P-256) and a self-signed certificate for `office.wehelp.invalid` and, once installed, `<office-id>.wehelp.invalid` | Phones pin the key, through the Relay (#36, #37) |
| `ca.key` / `ca.crt` | Local CA (P-256, 10 years, path length 0) | Staff browsers trust it for the staff UI on the LAN (#38) |
| `registration.json` (mode 600) | Office ID, Relay addresses and host key, Registry URL and signing key | The office's connection to its Relay (#4) and the roster (#62) |

**The key pin** is the base64 SHA-256 of the office TLS public key in
uncompressed X9.63 form (`04 || X || Y`). Those are exactly the
`subjectPublicKey` bits of the certificate's SPKI, and the bytes iOS
`SecKeyCopyExternalRepresentation` returns, so the app checks the pin without
parsing ASN.1 (spike S1). The certificate itself can be reissued at any time
without changing the pin. `init` reissues it when it is missing, expiring
within 30 days, or naming the wrong host.

`init` prints a JSON summary with no private material: the SSH public key,
the key pin and, once installed, the pairing identity (Relay CONNECT address,
office ID, key pin) that the QR payload (#37) carries.

## Installing with the Registry

WeHelp support issues a single-use install token (docs/REGISTRY.md). On the
Back Office:

```bash
read -rs WEHELP_INSTALL_TOKEN && export WEHELP_INSTALL_TOKEN   # paste; not echoed or kept in history
WEHELP_REGISTRY_URL=https://registry.example deploy/appliance.sh init-office
unset WEHELP_INSTALL_TOKEN
```

Outside the appliance, run `wehelpd init --install-token …` or set
`WEHELP_INSTALL_TOKEN`. Install sends only the token and the office SSH
public key. The office then stores the Registry's answer after checking that
the signing key and the Relay host key parse and that the office ID is safe
to use as a host name.

## Rotation

Rotate a key when it may have been exposed, when the Mac mini is replaced or
re-imaged without its encrypted backup, or on the schedule in the security
policy. Each procedure moves the old file aside (never delete it before the
new one works) and runs `init` again, which creates only the missing key.

### Office SSH key

1. Ask WeHelp support for a new install token for the practice.
2. Stop the server. Move `ssh_ed25519.key` and `ssh_ed25519.pub` aside.
3. Run `init` with the token. The Registry keeps the office ID, replaces the
   key, and publishes a new office list. Relays drop the old key within a
   minute.
4. Start the server, check that it connects to its Relay, then destroy the
   old key.

Phones are unaffected: they pin the TLS key, not the SSH key.

### Office TLS key (phones re-pin)

Phones hold the old pin, so after this rotation every paired phone must
re-pair. Plan it with the practice.

1. Stop the server. Move `tls.key` and `tls.crt` aside.
2. Run `init`. It creates a new key and certificate and prints the new pin.
3. Start the server. Phones now fail the pin check and refuse to connect.
   They fail closed by design.
4. Staff re-pair each phone with **Pair phone** (#37), which carries the new
   pin in its QR code. Existing device keys can stay enrolled. Only the
   pinned office identity changes.

A pin-set rollover is not in V1. If needed later, it would let the app pin
the next key alongside the current one so rotation needs no re-pairing.

### Local CA

1. Move `ca.key` and `ca.crt` aside and run `init`.
2. Reissue the staff UI certificate (#38) and install the new `ca.crt` in
   staff browsers. Remove the old CA from them.

### Registry signing key

The Registry signing key is rotated by WeHelp (docs/REGISTRY.md). Afterwards,
update `registry_signing_key` in `registration.json`: reinstall with a new
token, or edit the file and keep it mode 600.
