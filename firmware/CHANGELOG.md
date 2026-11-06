# Changelog

## [2.0.0](https://github.com/Zetesis-Labs/Sebastian/compare/firmware-v1.5.0...firmware-v2.0.0) (2026-11-06)


### Features

* **firmware:** listen to a playback while idle ([2d35896](https://github.com/Zetesis-Labs/Sebastian/commit/2d35896bcdca48ce571374b5f5484e1a6a8179a7))
* **firmware:** playback rooms decode stereo ([b124955](https://github.com/Zetesis-Labs/Sebastian/commit/b1249556b9502d78db07c75a092048a84e262948))


### Bug Fixes

* **firmware:** a LiveKit room can subscribe without publishing ([98456b4](https://github.com/Zetesis-Labs/Sebastian/commit/98456b4fb049b9159628dc6043c2e92eaac00a88))
* **firmware:** a room that only listens receives Opus ([e96bed1](https://github.com/Zetesis-Labs/Sebastian/commit/e96bed1165bacf574c29cdf45f8aa77bf84e5aed))
* **firmware:** music in a playback room plays smoothly ([6ab8462](https://github.com/Zetesis-Labs/Sebastian/commit/6ab8462405a41401c6b8c69d2a3ef516a60613f4))
* **firmware:** pin esp_muxer to the lock's version ([f9ad920](https://github.com/Zetesis-Labs/Sebastian/commit/f9ad9209a6dee43868a481d32964916802722753))
* **firmware:** the LiveKit engine's stack and queue live in PSRAM ([5a2cdb4](https://github.com/Zetesis-Labs/Sebastian/commit/5a2cdb46c217c9e15cab3b24be6e03064cfb755f))
* **firmware:** the poll's playback header is parsed in the core ([752e480](https://github.com/Zetesis-Labs/Sebastian/commit/752e48063056053655a1b2d7b8596469410a07de))
* **firmware:** the wake word detector runs on core 1 ([2cb7c24](https://github.com/Zetesis-Labs/Sebastian/commit/2cb7c24c2e4e06d8fc754c0e8f1229dd9e18cc36))

## [1.5.0](https://github.com/Zetesis-Labs/Sebastian/compare/firmware-v1.4.0...firmware-v1.5.0) (2026-11-04)


### Miscellaneous Chores

* **firmware:** Synchronize dashboard and firmware versions

## [1.4.0](https://github.com/Zetesis-Labs/Sebastian/compare/firmware-v1.3.0...firmware-v1.4.0) (2026-11-04)


### Features

* **firmware:** say the announcements the control room orders ([7df7c06](https://github.com/Zetesis-Labs/Sebastian/commit/7df7c06544c53b0ddbd27f79468e5fe2286b6107))

## [1.3.0](https://github.com/Zetesis-Labs/Sebastian/compare/firmware-v1.2.0...firmware-v1.3.0) (2026-11-04)


### Features

* **firmware:** log heap fragmentation and failed allocations ([14fef8c](https://github.com/Zetesis-Labs/Sebastian/commit/14fef8c700a6374d46e216669a1b3ecea64b03b1))
* **firmware:** opt-in heap trace that names the blocks outliving a session ([34cb363](https://github.com/Zetesis-Labs/Sebastian/commit/34cb3633d0d65db0a71b5a4a61172621dbc0bf42))


### Bug Fixes

* **firmware:** allocate mDNS from PSRAM again ([80267da](https://github.com/Zetesis-Labs/Sebastian/commit/80267dab187bfca9a054c4bc31b8fc12e7788911))
* **firmware:** pin the component versions the lock cannot hold ([b262ee1](https://github.com/Zetesis-Labs/Sebastian/commit/b262ee13d1bbeb3b1f0da7cca40c0cf68b523506))
* **firmware:** reset the speaker renderer once the session has drained ([499a828](https://github.com/Zetesis-Labs/Sebastian/commit/499a8282ca011b9defeae5186cefcfc87cc4a48c))
