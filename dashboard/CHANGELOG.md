# Changelog

## [1.1.1](https://github.com/Zetesis-Labs/Sebastian/compare/dashboard-v1.1.0...dashboard-v1.1.1) (2026-09-24)


### Bug Fixes

* **dashboard:** build the web installer with pnpm 12 and approve esbuild ([#54](https://github.com/Zetesis-Labs/Sebastian/issues/54)) ([509979f](https://github.com/Zetesis-Labs/Sebastian/commit/509979f39e42460b6c7f4d2ea165fa613e544297))
* **dashboard:** pin pnpm 9 for the embedded web installer build ([#52](https://github.com/Zetesis-Labs/Sebastian/issues/52)) ([cc625f7](https://github.com/Zetesis-Labs/Sebastian/commit/cc625f70268583f11b55471e16d2aa3674d10f24))

## [1.1.0](https://github.com/Zetesis-Labs/Sebastian/compare/dashboard-v1.0.0...dashboard-v1.1.0) (2026-09-22)


### Features

* **dashboard,server:** meeting recordings block E — the control room (RM-14/22/23/24/31/40..47) ([3ec5a7b](https://github.com/Zetesis-Labs/Sebastian/commit/3ec5a7b63566e7bbadb05c87f641f33a458a108a))
* **dashboard:** fleet view with adoption, device page with running vs desired config, embedded installer ([2102508](https://github.com/Zetesis-Labs/Sebastian/commit/21025083f28699e0643cf8c46a582f65d41b9053))
* **dashboard:** the ficha's form follows the profile — no duplex mode nor conversation for a micro-usb unit ([18be31f](https://github.com/Zetesis-Labs/Sebastian/commit/18be31ffd0d7398e80cf80f4b0c0e2a3937a6179))
* **dashboard:** the ficha's form shows only what the control room governs, grouped and pre-filled ([795a1df](https://github.com/Zetesis-Labs/Sebastian/commit/795a1df53c1182d1f25196221fc17b6be4388989))
* **firmware,server:** meeting recordings block B — MUTE gestures, LAN/poll orders, red ring, meeting session (RM-01/02/03/10/11/20/26/50/52) ([329b3af](https://github.com/Zetesis-Labs/Sebastian/commit/329b3afc07a2b471500d4b5a5f4f88260dcb2064))
* **fleet:** born adopted — a unit provisioned from the embedded installer enrols itself (RF-03/RF-51) ([1bb36bd](https://github.com/Zetesis-Labs/Sebastian/commit/1bb36bd43658875ed286ab8d60815dd2ce432d81))
* **fleet:** close the functional spec — unit events, forget in the ficha, QR, firmware version, legacy /token retired, outbox events ([c3031bb](https://github.com/Zetesis-Labs/Sebastian/commit/c3031bb31088a4944d8c0c90c2174454ef87c896))
* **fleet:** explicit transition states (joining / moved / leaving) and a clearer device row ([6cad51e](https://github.com/Zetesis-Labs/Sebastian/commit/6cad51eb6de7cc91d5ee984c18c5fcd8c2d8531d))
* **fleet:** the device secret is born with the unit; adopting hands it over, rotation is optional (RF-51..53) ([bf049c6](https://github.com/Zetesis-Labs/Sebastian/commit/bf049c694708be59e802b22f1fa8288b905eee7c))
* **fleet:** the unit reports the configuration it runs; the ficha shows running vs desired field by field (RF-42) ([d914ce5](https://github.com/Zetesis-Labs/Sebastian/commit/d914ce55ec6bea0bed0d35f5e9afdae66d490d48))
* **server:** meeting recordings block D — transcription by pieces, diarized speakers, summary, retention (RM-30..35/45/46) ([5e7f8e0](https://github.com/Zetesis-Labs/Sebastian/commit/5e7f8e0c25fea9805530bd71dcf9bc7bd262dfa3))


### Bug Fixes

* **dashboard,web-installer:** the device ficha never rendered; the installer died on a missing adoption section ([793acf8](https://github.com/Zetesis-Labs/Sebastian/commit/793acf8c72516c210a47758aacece446f54552bc))
* **dashboard:** a rejected config is no news once nothing is desired; hardware notes ([ffb365e](https://github.com/Zetesis-Labs/Sebastian/commit/ffb365e429416dbcf4d19972321d93b6ddc70b36))
* **dashboard:** Recuperar asks for the device secret; retry with it after an auth rejection ([7a99361](https://github.com/Zetesis-Labs/Sebastian/commit/7a9936162a922b5ab3c05da99b1115bf5ccd3f47))
* **dashboard:** the mode selector corrupted the desired document; only governable fields travel ([89e1164](https://github.com/Zetesis-Labs/Sebastian/commit/89e1164e120aed1e4c394a3e4c5e6fc69e546374))
* **firmware:** a unit with a born secret but no organization secret and no control room asks for MUTE consent (RF-34) ([f21e4a7](https://github.com/Zetesis-Labs/Sebastian/commit/f21e4a7ea2d628132da6b00d5c5d6286c2715d19))
* **fleet:** a poll newer than a stale announce wins; device-secret field for handover (RF-38) ([ec72596](https://github.com/Zetesis-Labs/Sebastian/commit/ec72596bf94c823ae9d04384a1e3201146300ec2))

## 1.0.0 (2026-07-27)


### Features

* add sebastian server and admin dashboard ([ab0d8dc](https://github.com/Zetesis-Labs/Sebastian/commit/ab0d8dca5faaafe74ae558263b48a85e4b0bda56))
* add Sebastian server and admin dashboard ([ed9cfbc](https://github.com/Zetesis-Labs/Sebastian/commit/ed9cfbc3794d415ae9d6d646966ce82af5102d91))
* **firmware:** provisioning profiles — one binary, agent + USB-mic personalities ([5774921](https://github.com/Zetesis-Labs/Sebastian/commit/5774921126facc1a74d2c30a894086a4c0a8b374))
* network control + convivencia (F3/F4, RAM fix, AGC tuning) ([2cced41](https://github.com/Zetesis-Labs/Sebastian/commit/2cced4168beb321b3fa9dc45427af094d44559f6))
* network control of device profiles (F3 telemetry + F4 desired-state) ([3af8161](https://github.com/Zetesis-Labs/Sebastian/commit/3af8161b7a19d23dec4bb2f3c3f01f17e6de4ced))
* **server:** device desired-profile reconciliation + dashboard fleet view ([e3087be](https://github.com/Zetesis-Labs/Sebastian/commit/e3087bec057a48b388e5769276cb200774c7154c))


### Bug Fixes

* **ci:** relative livekit override path + patched dev deps ([7ada598](https://github.com/Zetesis-Labs/Sebastian/commit/7ada598737e3b1ab593c356d74f2107e8f0bb8b7))
* **ci:** relative livekit override path, zig fmt, and patched dev deps ([f255976](https://github.com/Zetesis-Labs/Sebastian/commit/f2559764b18484a4285e1ceb230499568249cb04))
* **dashboard:** auto-refresh the devices view every 10 s ([28cb388](https://github.com/Zetesis-Labs/Sebastian/commit/28cb3886b613132785d428e1e3724196d2b252e2))

## Changelog

All notable changes to the Sebastian Dashboard will be documented in this file.
