# AGENTS.md

Native Pulumi provider for [Railway](https://railway.com), written in Go on
`pulumi-go-provider`, with a generated TypeScript SDK published as
`@thegreataxios/pulumi-railway`.

## Layout

- `provider/` — Go module. `pkg/` holds the provider: `provider.go` (config +
  metadata), `client.go` (Railway GraphQL client), one file per resource
  (`project.go`, `service.go`, `variable.go`, `custom_domain.go`, `volume.go`),
  `validation.go` (shared Check helpers), `*_test.go` (httptest-mocked
  GraphQL).
- `schema.json` — **generated**, never hand-edit (`make schema`).
- `sdk/nodejs/` — **generated**, never hand-edit (`make sdk` overwrites it,
  including `package.json`). Package metadata comes from
  `provider/pkg/provider.go` (`WithLanguageMap` nodejs block) plus
  `scripts/prepare-node-package.mjs`, which injects author/bugs/engines and
  the real version at pack time. The committed `package.json` keeps the
  `0.0.0-dev` placeholder version (the `sdk` target normalizes it) so
  `check-generated` is stable across release versions. The single committed
  lockfile is `sdk/nodejs/package-lock.json` (`make nodejs-lock` refreshes it).
- `examples/simple/` — smoke-test stack (local SDK via `file:` dep).
- `.github/workflows/` — `ci.yml` on PRs, `release.yml` on semantic `v*.*.*` tags
  (GoReleaser archives), `publish-npm.yml` manual dispatch to publish the npm
  package for a tag (needs the `NPM_TOKEN` secret).

## Rules

- Run `make ci VERSION=x.y.z` before considering anything done. It runs Go
  tests with `-race`, `go vet`, golangci-lint, generated artifact drift checks,
  SDK packaging, a binary version assertion, and the example typecheck.
- Resource tokens live under `railway:index:*` with `railway:pkg:*` aliases.
  Do not rename tokens or properties without an alias — that breaks state.
- Immutable identity/source fields carry `replaceOnChanges`. Adding a mutable
  field means implementing it in `Update`; adding an immutable one means
  adding the tag.
- Unknown/computed inputs must pass `Check` — previews send unknowns.
- `ProviderConfig.Configure` must keep a value receiver. pulumi-go-provider
  asserts the config value against `CustomConfigure`; a pointer receiver
  silently never runs and CRUD proceeds without a client. The shared API
  client lives in the credential-keyed `clientCache`, not on the config.
- Never commit tokens or Railway project/environment IDs belonging to a real
  account. Tests use httptest mocks; examples use placeholders.

## Railway API notes

- Endpoint `https://backboard.railway.com/graphql/v2`; account tokens use
  `Authorization: Bearer`, project tokens use `Project-Access-Token`.
- The GraphQL schema as documented drifts from reality — verify field names
  against the live API (Railway CLI OAuth token works for introspection)
  before trusting docs. Known mismatches are encoded in the resource files
  (e.g. `primaryEnvironmentId`, top-level `branch`, variable `name`).

## Release

Tag `vX.Y.Z`; npm version and tag must match. The release workflow runs the
full suite and publishes binaries to GitHub releases. Publishing the npm
package is a separate manual step: run the `Publish npm` workflow
(`publish-npm.yml`) with the tag, which publishes with provenance.
