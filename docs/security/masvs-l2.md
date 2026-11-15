# OWASP MASVS v2 checklist (iOS app)

The WeHelp iOS app against [OWASP MASVS v2](https://mas.owasp.org/MASVS/),
all 24 controls including MASVS-RESILIENCE (#44). Control IDs are OWASP's;
the short descriptions are ours. Reviewed 2026-11-15 against `main` at the
merge of #158.

Each row: the requirement ID, the requirement in our own words (the official
text is in the standard), a status, and the evidence.

- **Met:** a control exists and was checked in the code, a test or a doc.
- **N/A:** the requirement can't apply to WeHelp (reason given).
- **Accepted:** deliberately not done, with the reasoning.
- **Gap:** not met, or couldn't be confirmed. Every gap has an issue.

Components: Back Office (`wehelpd`, staff web), Relay, Registry (and its
console), iOS app. A row is Met only if it holds for every component it
applies to.

**Totals:** 9 Met, 1 N/A, 2 Accepted, 12 Gap.

## Controls

Scope: the iOS app (`ios/WeHelp`, `ios/project.yml`, `ios/WeHelp/Info.plist`, `ios/WeHelp/WeHelp.entitlements`, `scripts/check-ios-release.sh`). Level: MASVS L2, the full v2 control set, MASVS-RESILIENCE included.

| ID | Requirement (your words) | Status | Evidence / reasoning |
|---|---|---|---|
| MASVS-STORAGE-1 | Sensitive data the app stores on purpose is protected | Gap | Refresh token and Secure Enclave key blob are Keychain items with `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly` (`Services/Keychain.swift` `set`), and the access token stays in memory (`Services/SessionStore.swift`). But `AppState.persist()` writes `[Account]` to UserDefaults, and that list includes the patient's `displayName` with the `practice` name (`Models/Account.swift`, `AppState.pair`). A patient's name tied to a practice is PHI, which contradicts "no PHI at rest" in docs/MOBILE_CLIENT.md §9. |
| MASVS-STORAGE-2 | Sensitive data doesn't leak through backups, logs or caches | Gap | Good: `URLSessionConfiguration.ephemeral` means no disk cache (`Services/OfficeTransport.swift`), the app has no `print`/`Logger`/`NSLog` calls, and Keychain items are `ThisDeviceOnly`. Problems: the UserDefaults account list (patient name and practice) goes into iCloud/Finder backups, and the PHI text fields (`MessagesView.swift:146`, `AppointmentRequestView.swift:65`) don't turn off autocorrection, so the keyboard can learn what patients type. |
| MASVS-CRYPTO-1 | Uses current strong crypto in standard ways | Met | The app uses CryptoKit P-256 ECDSA over SHA-256 (`Services/DeviceKey.swift` `sign`), SHA-256 key pins (`Pinning.matches`) and TLS 1.3 on both hops (`relayTLSOptions`, plus the ATS exception's minimum). There is no custom crypto. |
| MASVS-CRYPTO-2 | Keys are generated, stored and protected properly | Met | Each account's key is generated inside the Secure Enclave, so it can't be exported. The Keychain holds only the Enclave's opaque blob (`DeviceKey.create`). Software keys exist only under `#if targetEnvironment(simulator)`. Keys are per account, and `Keychain.deleteAll` removes them when a device is revoked (`AppState.removeRevoked`). |
| MASVS-AUTH-1 | Remote authentication and authorization follow best practice | Met | Access tokens live 15 min, in memory only (`access_ttl` in `internal/config/config.go`). Refresh tokens are single-use and need a fresh ECDSA device proof with a timestamp on every refresh (`DeviceKey.proof`, `internal/auth/devicekey.go`, `TestDeviceProof`, `TestRefreshRetryAndCodes`). Refreshes are single-flight (`SessionStore.refresh`). The server enforces authorization. |
| MASVS-AUTH-2 | Local biometric or passcode check is done securely | Gap | `AppLock.unlock` uses `LAContext.evaluatePolicy(.deviceOwnerAuthentication)`, locks on every trip to the background, and in Release refuses to run without a passcode (`AppLock.swift`, `WeHelpApp.swift`). But the check only flips a boolean in the UI: the Keychain items and the Enclave key have no `SecAccessControl` (user presence), so hooking the app gets past the lock. |
| MASVS-AUTH-3 | Sensitive operations need extra authentication | Met | Enrolling a device needs a single-use, 10-minute invite from the in-office QR code plus proof of the new device key (`APIClient.redeemInvite`, `PairingPayload.parse`). Every token refresh needs the device key. The app can't revoke devices: `DevicesView.swift` is read-only, and only the practice revokes. |
| MASVS-NETWORK-1 | All network traffic is secured to current practice | Met | Every Back Office request goes through `OfficeTransport`: an HTTP CONNECT proxy over TLS 1.3 to the Relay, and inside it TLS end to end to `https://<office>.wehelp.invalid/` (`Models/OfficeEndpoint.swift` `baseURL`). Video is DTLS-SRTP with relay-only ICE (`VisitCall.swift:166`). `check-ios-release.sh` fails on any arbitrary or local-network loads and on any ATS exception other than `wehelp.invalid`; the one exception is explained in the ATS note below. |
| MASVS-NETWORK-2 | Pins the identity of the project's own servers | Accepted | Both the Relay key (`Pinning.relayTLSOptions`) and the office key (`OfficePinningDelegate`) are pinned and fail closed with no override (`APIError.pinMismatch`). TURN over TLS to the Relay is not pinned, because libwebrtc can't pin. docs/SECURITY.md "Telehealth" accepts this: DTLS fingerprints travel over the pinned signaling and are checked (`verifyFingerprint`, `DTLSFingerprintTests`). |
| MASVS-PLATFORM-1 | Inter-process communication is used safely | Met | The app registers no URL schemes and no associated domains (Info.plist, entitlements), has no extensions, and has no document or file sharing (`check-ios-release.sh` checks the last one). It reads the pasteboard only when the user taps "Paste code", and validates the content strictly (`PairingView.swift:105`). |
| MASVS-PLATFORM-2 | WebViews are configured safely | N/A | The app has no WKWebView or SFSafariViewController. Podcast links open in the system browser, https only (`LibraryView.swift`). |
| MASVS-PLATFORM-3 | The UI doesn't leak sensitive data | Met | `PrivacyCover` hides signed-in screens whenever the app isn't active (the app-switcher snapshot), and `LockView` covers them after a trip to the background (`WeHelpApp.swift`). PHI views are marked `.privacySensitive()`. Pushes carry fixed generic text (`PushNotifications.swift`, docs/SECURITY.md "Push"). |
| MASVS-CODE-1 | Requires an up-to-date OS version | Gap | `deploymentTarget iOS: "17.0"` (`ios/project.yml`) is several major releases behind, and nothing enforces a minimum patch level, so unpatched iOS 17.x devices are accepted. |
| MASVS-CODE-2 | Can force users onto a fixed app version | Gap | Neither the app nor the Back Office has a minimum-version check or an "update required" response. Searches of `ios/WeHelp` and `internal/` for an app-version gate found none. |
| MASVS-CODE-3 | Third-party components have no known vulnerabilities | Gap | WebRTC is pinned with `exactVersion: 157.0.0`, and the update policy is a comment in `ios/project.yml` (docs/spikes/S2.md). But no scanner covers Swift packages: CI's `govulncheck` covers Go only, so a WebRTC CVE depends on someone noticing it. |
| MASVS-CODE-4 | Validates every untrusted input | Met | Pairing text must be a typed v1 JSON payload with a UUID office ID, 32-byte pins, `host:port` and an invite of 16–64 characters (`PairingPayload.parse`, `OfficeEndpoint.problem`). Responses are decoded with typed `Codable`. Podcast URLs must be https. Library filenames come from the pinned office, which cleans them with `site.CleanFilename`; the app doesn't check them again. |
| MASVS-RESILIENCE-1 | Checks that the OS hasn't been tampered with | Accepted | docs/SECURITY.md "Who we defend against" puts "jailbroken devices with root on the patient's own phone" out of scope for V1. Issue #11 also rules out jailbreak detection. |
| MASVS-RESILIENCE-2 | Detects or prevents a modified app | Gap | The only protection is App Store code signing. App Attest is still undecided (issue #11 open; docs/SECURITY.md "Open security decisions"). |
| MASVS-RESILIENCE-3 | Makes static analysis of the app harder | Gap | The app has no obfuscation, and no recorded decision to skip it. `check-ios-release.sh` only strips DEBUG and demo strings. |
| MASVS-RESILIENCE-4 | Makes runtime analysis and instrumentation harder | Gap | The app has no debugger or instrumentation detection, and no recorded decision to skip it. The jailbreak scope note covers only the patient's own rooted phone. |
| MASVS-PRIVACY-1 | Asks only for the data and permissions it needs | Gap | The HealthKit entitlement and the HealthKit and Bluetooth usage strings ship, but no code uses HealthKit or CoreBluetooth (a search of `ios/WeHelp` finds neither). `NSLocalNetworkUsageDescription` is there for the Demo topology only, but Release builds ship it too. Camera and microphone are used (QR scanning, visits). There are no third-party SDKs. |
| MASVS-PRIVACY-2 | Prevents identifying or tracking the user | Met | The app has no analytics, advertising or crash SDKs. Account IDs are random UUIDs per pairing, and each practice gets its own device key, so practices can't link a device. The Relay sees no device IP in its logs (`TestRelayLogsNoDeviceAddress`). |
| MASVS-PRIVACY-3 | Is open about what data it collects and why | Gap | The app has no `PrivacyInfo.xcprivacy` privacy manifest, which App Store builds need because the app uses UserDefaults, a required-reason API. The HealthKit and Bluetooth usage strings describe features the app doesn't have. |
| MASVS-PRIVACY-4 | Lets users manage or delete their data | Gap | Removing a practice from the phone is deliberately the practice's job (product decision 2026-11: devices are never unpaired from the app; the practice revokes, and the app then drops the account, `AppState.removeRevoked`). Remaining gap: deleting the app leaves its Keychain items (refresh token, key blob) on the phone. |

**ATS exception.** The app has exactly one App Transport Security exception: `wehelp.invalid` with `NSIncludesSubdomains`, `NSExceptionAllowsInsecureHTTPLoads` and `NSExceptionMinimumTLSVersion: TLSv1.3` (`ios/WeHelp/Info.plist`, `ios/project.yml`).
- **Why it's needed.** Default ATS rejects the office's pinned self-signed certificate even when the `URLSession` delegate anchors trust to it with `SecTrustSetAnchorCertificates`. CFNetwork runs its own system trust check and fails with `-1200` (docs/spikes/S1.md, finding 1).
- **Why it's acceptable:**
  - `.invalid` is reserved (RFC 2606) and resolves nowhere, so these names are reachable only through the Relay's CONNECT proxy.
  - The Relay key and the office key are both pinned, and both fail closed (`Pinning.relayTLSOptions`, `OfficePinningDelegate`, `APIError.pinMismatch`).
  - TLS 1.3 is still required.
  - No other domain is affected (docs/MOBILE_CLIENT.md, docs/SECURITY.md "Phone ↔ Back Office").
- **CI.** `scripts/check-ios-release.sh` fails a Release build if any other exception, an arbitrary-loads key or `NSAllowsLocalNetworking` appears, or if `wehelp.invalid` doesn't require TLS 1.3.
- **Residual risk.** `NSExceptionAllowsInsecureHTTPLoads` would also allow plain `http://` to that domain. Today the only URL builder is `https://` (`OfficeEndpoint.baseURL`), but no test or check guards that.

## Gaps and where they're tracked

| Row | Tracked in |
|---|---|
| MASVS-STORAGE-1 | #164 |
| MASVS-STORAGE-2 | #164 |
| MASVS-AUTH-2 | #163 |
| MASVS-CODE-1 | #165 |
| MASVS-CODE-2 | #165 |
| MASVS-CODE-3 | #165 |
| MASVS-RESILIENCE-2 | #11 |
| MASVS-RESILIENCE-3 | #11 |
| MASVS-RESILIENCE-4 | #11 |
| MASVS-PRIVACY-1 | #164 |
| MASVS-PRIVACY-3 | #164 |
| MASVS-PRIVACY-4 | #164 |

### Notes from the review

- **MASVS-STORAGE-1** (iOS): stop persisting `displayName` and `practice` in UserDefaults. Keep the account list in the Keychain (`ThisDeviceOnly`), or keep only opaque IDs and fetch names on view.
- **MASVS-STORAGE-2** (iOS):
- **MASVS-AUTH-2** (iOS): bind the unlock to the Keychain. Give the refresh-token item or the Enclave key a `SecAccessControl` with `.userPresence`, read with the `LAContext` that passed.
- **MASVS-CODE-1** (iOS): raise `deploymentTarget` to a currently patched iOS major version and record the policy in docs/MOBILE_CLIENT.md.
- **MASVS-CODE-2** (iOS + Back Office): have the app send its version on each request. The Back Office answers below a configured minimum with an `update_required` code, and the app shows a blocking "Update WeHelp" screen.
- **MASVS-CODE-3** (iOS, CI): add a Swift Package scan for known vulnerabilities (OSV or GitHub dependency review on `Package.resolved`) to the CI ios job.
- **MASVS-RESILIENCE-2** (iOS): decide issue #11 (App Attest at enrollment), and record adopt or reject in docs/LIT_REVIEW.md and docs/SECURITY.md.
- **MASVS-RESILIENCE-3** (iOS): record a decision on obfuscation (likely Accepted: no secrets in the binary, and the server enforces authorization) in docs/SECURITY.md.
- **MASVS-RESILIENCE-4** (iOS): record a decision on anti-debug and anti-instrumentation (possibly folded into the #11 App Attest decision) in docs/SECURITY.md.
- **MASVS-PRIVACY-1** (iOS): remove the HealthKit entitlement and the HealthKit and Bluetooth usage strings until a feature uses them, and keep `NSLocalNetworkUsageDescription` out of Release.
- **MASVS-PRIVACY-3** (iOS): add `PrivacyInfo.xcprivacy` declaring UserDefaults (required reason CA92.1) and the data collected, and make the purpose strings match the real features.
- **MASVS-PRIVACY-4** (iOS): on a first launch with an empty account list, delete leftover Keychain items. (Removing a practice from the phone stays the practice's job, by decision.)
  - fix MASVS-STORAGE-1.
