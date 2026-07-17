# Changelog

All notable changes to Sebastian Server will be documented in this file.

## [Unreleased]

- Add a transactional outbox worker publishing deduplicated CloudEvents to NATS
  JetStream with persistent retry backoff.
