# Changelog

## [0.1.5](https://github.com/Zetesis-Labs/Sebastian/compare/agent-v0.1.4...agent-v0.1.5) (2026-07-13)


### Features

* **agent:** endpointing por inactividad con cierre device-initiated ([dfb5c75](https://github.com/Zetesis-Labs/Sebastian/commit/dfb5c7551513bc5e80313ac46344416f9d51ce25))
* **agent:** full-duplex talk-over interruptions + immediate device flush ([7787cf3](https://github.com/Zetesis-Labs/Sebastian/commit/7787cf38ce530364dfe7ee1d56f695f4fde2800e))
* **agent:** record the model-input audio per session (_model.wav) ([233be86](https://github.com/Zetesis-Labs/Sebastian/commit/233be86e4b17b4510c3fe602db9295118d6ce91e))
* **agent:** verificación en dos etapas del wake word sobre el pre-roll ([9a7fb3e](https://github.com/Zetesis-Labs/Sebastian/commit/9a7fb3efaaa6bf28452ea31bd819a382495970a6))
* endpointing server-side — el agente decide el fin de la conversación ([6a61a26](https://github.com/Zetesis-Labs/Sebastian/commit/6a61a2684a3a5c81b5298bd05319163564db825c))
* wake word split — recall en placa, precisión en servidor ([3ffa8a4](https://github.com/Zetesis-Labs/Sebastian/commit/3ffa8a426f66c76535b09e7570be4d7cfaec00a8))


### Bug Fixes

* mic micro-cuts — device capture fixes + model-input recording ([ffc0818](https://github.com/Zetesis-Labs/Sebastian/commit/ffc0818a5f46855cf2844f080f99ca134beaf9fa))


### Documentation

* **roadmap:** re-priorización 2026-07-11 (foco altavoz puro) ([9f4621b](https://github.com/Zetesis-Labs/Sebastian/commit/9f4621be3de4b6e06f29287dd031714c2ab15a98))

## [0.1.4](https://github.com/Zetesis-Labs/Sebastian/compare/agent-v0.1.3...agent-v0.1.4) (2026-07-10)


### Features

* **agent:** logs OTLP en token-server y control-plane + paridad de logs en dev ([516e2d4](https://github.com/Zetesis-Labs/Sebastian/commit/516e2d469f70bf81976a0921822917a31e4dee93))
* **agent:** paridad de logs dev↔prod (OTLP en token/control + promtail LiveKit) ([871f888](https://github.com/Zetesis-Labs/Sebastian/commit/871f8882e23e4d624b12e15ab555835e86a74072))

## [0.1.3](https://github.com/Zetesis-Labs/Sebastian/compare/agent-v0.1.2...agent-v0.1.3) (2026-07-09)


### Features

* **agent:** control-plane announce scaffolding (per-session delivery) ([485413f](https://github.com/Zetesis-Labs/Sebastian/commit/485413f4cbcd68394d7747565eabc3aa18e31eff))

## [0.1.2](https://github.com/Zetesis-Labs/Sebastian/compare/agent-v0.1.1...agent-v0.1.2) (2026-07-09)


### Bug Fixes

* cut 0.1.2, superseding the interrupted 0.1.1 release ([b9487f4](https://github.com/Zetesis-Labs/Sebastian/commit/b9487f481520f212e88323d442facf931788a8a3))

## [0.1.1](https://github.com/Zetesis-Labs/Sebastian/compare/agent-v0.1.0...agent-v0.1.1) (2026-07-09)


### Features

* **agent:** configurable realtime provider + Gemini auto-detect language ([55b869f](https://github.com/Zetesis-Labs/Sebastian/commit/55b869fd8a9472faa96a3de8c03e6c6ddb7501da))
* **agent:** multilingual instructions, stop≠farewell, phantom-session signal ([2644538](https://github.com/Zetesis-Labs/Sebastian/commit/2644538fe3a9222f218952d11aab0256c1e04751))
* **agent:** two-sided session recordings (mic + agent voice) ([#9](https://github.com/Zetesis-Labs/Sebastian/issues/9)) ([823cfd5](https://github.com/Zetesis-Labs/Sebastian/commit/823cfd5ea15c78045431716e60536f87f192ed5c))
* operating modes in the web installer + runtime device config ([55a974e](https://github.com/Zetesis-Labs/Sebastian/commit/55a974e7e69ebc9455a6eac30a47e2c3ffadb6b4))


### Bug Fixes

* **agent:** bound the audio queue + retain fire-and-forget tasks (audit [#12](https://github.com/Zetesis-Labs/Sebastian/issues/12), [#2](https://github.com/Zetesis-Labs/Sebastian/issues/2).15) ([58bd7c4](https://github.com/Zetesis-Labs/Sebastian/commit/58bd7c4e428a9c0651d353712631e89ea7c4fad0))
* **agent:** dispatch at most one agent per room ([c05279b](https://github.com/Zetesis-Labs/Sebastian/commit/c05279b4906dd71f8ed6dfb4da9e4ff9fc05dd12))
* **agent:** instant barge-in — no warmup window, interrupt signal, stop≠farewell ([dd6f27a](https://github.com/Zetesis-Labs/Sebastian/commit/dd6f27a58afe95efb8247d7a123811f42aa0ef5c))
* **agent:** nudge race causing zombie thinking + BVC silently dead on self-host ([b12a05c](https://github.com/Zetesis-Labs/Sebastian/commit/b12a05c387fe8b18921e69d44af4142557f7e663))
* **agent:** token server self-heals zombie agent-only rooms ([293b6fb](https://github.com/Zetesis-Labs/Sebastian/commit/293b6fb54bfb173bcea9264e818c6f340a326874))


### Documentation

* Translate all Spanish documentation to English ([84e94d8](https://github.com/Zetesis-Labs/Sebastian/commit/84e94d8a89d2cbe81c266b1ad6d603278500f16d))
* Translate Spanish documentation to English ([0db5a0e](https://github.com/Zetesis-Labs/Sebastian/commit/0db5a0ef09fc13e05713767822c693ae29262f9f))
