# Changelog

All notable changes to Sebastian Server will be documented in this file.

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
