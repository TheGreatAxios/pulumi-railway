# Changelog

All notable changes to this project are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-08-20

Initial experimental release.

### Added

- Native Railway resources for projects, services, variables, custom domains,
  and volumes.
- CRUD, refresh, import, secret propagation, and replacement semantics for
  immutable identity and source fields.
- A generated TypeScript/Node.js SDK and GitHub-hosted provider binaries.

### Known limitations

- The provider is experimental and implements a subset of Railway.
- Only the TypeScript/Node.js SDK is published. Other Pulumi SDKs can be
  generated locally from the provider schema.
- Environment management, deployment triggers, TCP proxies, Railway-provided
  domains, and lookup functions are not yet implemented.
- Live integration tests are not yet run in CI.

[Unreleased]: https://github.com/thegreataxios/pulumi-railway/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/thegreataxios/pulumi-railway/releases/tag/v0.1.0
