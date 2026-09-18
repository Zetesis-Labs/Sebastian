# Changelog

All notable changes to Sebastian Server will be documented in this file.

## [1.1.0](https://github.com/Zetesis-Labs/Sebastian/compare/server-v1.0.0...server-v1.1.0) (2026-09-18)


### Features

* **dashboard:** fleet view with adoption, device page with running vs desired config, embedded installer ([2102508](https://github.com/Zetesis-Labs/Sebastian/commit/21025083f28699e0643cf8c46a582f65d41b9053))
* **fleet:** born adopted — a unit provisioned from the embedded installer enrols itself (RF-03/RF-51) ([1bb36bd](https://github.com/Zetesis-Labs/Sebastian/commit/1bb36bd43658875ed286ab8d60815dd2ce432d81))
* **fleet:** close the functional spec — unit events, forget in the ficha, QR, firmware version, legacy /token retired, outbox events ([c3031bb](https://github.com/Zetesis-Labs/Sebastian/commit/c3031bb31088a4944d8c0c90c2174454ef87c896))
* **fleet:** explicit transition states (joining / moved / leaving) and a clearer device row ([6cad51e](https://github.com/Zetesis-Labs/Sebastian/commit/6cad51eb6de7cc91d5ee984c18c5fcd8c2d8531d))
* **fleet:** the device secret is born with the unit; adopting hands it over, rotation is optional (RF-51..53) ([bf049c6](https://github.com/Zetesis-Labs/Sebastian/commit/bf049c694708be59e802b22f1fa8288b905eee7c))
* **fleet:** the unit reports the configuration it runs; the ficha shows running vs desired field by field (RF-42) ([d914ce5](https://github.com/Zetesis-Labs/Sebastian/commit/d914ce55ec6bea0bed0d35f5e9afdae66d490d48))
* **server:** fleet view, LAN adoption jobs, desired config and per-device secrets ([4478660](https://github.com/Zetesis-Labs/Sebastian/commit/44786602be4612707c10bcd2e514a52e478a0575))
* **server:** meeting recordings block A — state machine, orders to the unit, streaming audio, outbox events (RM-03/05/06/07/15/22/24/25/52) ([3dd15f9](https://github.com/Zetesis-Labs/Sebastian/commit/3dd15f94007604ce50f09d80312ad8e717d62e2c))


### Bug Fixes

* **dashboard:** the mode selector corrupted the desired document; only governable fields travel ([89e1164](https://github.com/Zetesis-Labs/Sebastian/commit/89e1164e120aed1e4c394a3e4c5e6fc69e546374))
* **fleet:** a poll newer than a stale announce wins; device-secret field for handover (RF-38) ([ec72596](https://github.com/Zetesis-Labs/Sebastian/commit/ec72596bf94c823ae9d04384a1e3201146300ec2))
* **server:** keep the store error in the session 503 chain ([45f4bf7](https://github.com/Zetesis-Labs/Sebastian/commit/45f4bf750ac11366b732837d7a0e6c0e7891644a))
* **server:** mDNS browse cycles leaked two spinning goroutines each (550 % CPU after a day) ([858fedd](https://github.com/Zetesis-Labs/Sebastian/commit/858fedde2bd2e9296afd8187fd33ddf450a02b8b))

## 1.0.0 (2026-07-27)


### Features

* add sebastian server and admin dashboard ([ab0d8dc](https://github.com/Zetesis-Labs/Sebastian/commit/ab0d8dca5faaafe74ae558263b48a85e4b0bda56))
* add Sebastian server and admin dashboard ([ed9cfbc](https://github.com/Zetesis-Labs/Sebastian/commit/ed9cfbc3794d415ae9d6d646966ce82af5102d91))
* network control + convivencia (F3/F4, RAM fix, AGC tuning) ([2cced41](https://github.com/Zetesis-Labs/Sebastian/commit/2cced4168beb321b3fa9dc45427af094d44559f6))
* network control of device profiles (F3 telemetry + F4 desired-state) ([3af8161](https://github.com/Zetesis-Labs/Sebastian/commit/3af8161b7a19d23dec4bb2f3c3f01f17e6de4ced))
* **server:** device desired-profile reconciliation + dashboard fleet view ([e3087be](https://github.com/Zetesis-Labs/Sebastian/commit/e3087bec057a48b388e5769276cb200774c7154c))


### Bug Fixes

* **server:** eliminate zombie agent on session create failure ([ba36f83](https://github.com/Zetesis-Labs/Sebastian/commit/ba36f831aeab073a7afd4abb5b3ef305adc520ae))

## [Unreleased]

- Add a transactional outbox worker publishing deduplicated CloudEvents to NATS
  JetStream with persistent retry backoff.
