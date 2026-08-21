# @thegreataxios/pulumi-railway

[![npm](https://img.shields.io/npm/v/@thegreataxios/pulumi-railway)](https://www.npmjs.com/package/@thegreataxios/pulumi-railway)
[![CI](https://github.com/thegreataxios/pulumi-railway/actions/workflows/ci.yml/badge.svg)](https://github.com/thegreataxios/pulumi-railway/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](./LICENSE)

An experimental native Pulumi provider for a subset of
[Railway](https://railway.com): projects, services, environments, environment
variables, custom domains, volumes, and object-storage buckets. It provides
CRUD, drift detection, import, and replacement semantics on immutable fields.

## Status

This provider is pre-1.0 and is not listed in the Pulumi Registry. The
TypeScript/Node.js SDK is the only SDK published as a package. Deployment
triggers, Railway-provided domains, TCP proxies, and lookup functions are not
implemented yet.

Go, Python, and .NET users can generate local SDKs from the installed plugin:

```bash
pulumi package gen-sdk railway --language go
pulumi package gen-sdk railway --language python
pulumi package gen-sdk railway --language dotnet
```

## Supported resources

| Resource | Description |
| --- | --- |
| `railway.Project` | Railway project and default environment |
| `railway.Environment` | Named project environment (staging, previews) |
| `railway.Service` | Service source and per-environment runtime configuration |
| `railway.Variable` | Service or shared environment variable |
| `railway.CustomDomain` | Custom domain, DNS verification, and certificate status |
| `railway.Volume` | Persistent volume and mount path |
| `railway.Bucket` | S3-compatible object storage bucket |

## Installation

Requires the [Pulumi CLI](https://www.pulumi.com/docs/install/) and Node.js 20
or newer.

```bash
npm install @thegreataxios/pulumi-railway
```

The SDK downloads the matching `pulumi-resource-railway` binary from this
repository's GitHub releases.

## Authentication

Use one authentication method:

```bash
# Account or workspace token
pulumi config set --secret railway:token <token>

# Or a project token
pulumi config set --secret railway:projectToken <token>
```

The provider also reads `RAILWAY_API_TOKEN` for account/workspace tokens and
`RAILWAY_TOKEN` for project tokens. If both token types are configured, the
provider returns an error rather than choosing one implicitly.

Create account/workspace tokens at <https://railway.com/account/tokens>.

## Usage

```typescript
import * as railway from "@thegreataxios/pulumi-railway";

const project = new railway.Project("my-project", {
  name: "my-app",
  // Strongly recommended: projects created without a workspace ID are
  // temporary public projects that expire after 24 hours unless claimed.
  // Find yours in the Railway dashboard under workspace settings.
  workspaceId: "your-workspace-id",
});

const web = new railway.Service("web", {
  projectId: project.id,
  environmentId: project.defaultEnvironmentId,
  name: "web",
  image: "node:20-alpine",
  startCommand: "node server.js",
  healthcheckPath: "/health",
  numReplicas: 2,
});

new railway.Variable("database-url", {
  projectId: project.id,
  environmentId: project.defaultEnvironmentId,
  serviceId: web.id,
  key: "DATABASE_URL",
  value: "postgresql://...",
});

new railway.CustomDomain("api", {
  projectId: project.id,
  environmentId: project.defaultEnvironmentId,
  serviceId: web.id,
  domain: "api.example.com",
  targetPort: 8080,
});

new railway.Volume("data", {
  projectId: project.id,
  environmentId: project.defaultEnvironmentId,
  serviceId: web.id,
  mountPath: "/data",
  name: "app-data",
});

export const projectId = project.id;
export const serviceId = web.id;
```

Changing a service's repository, branch, project, or environment replaces the
service. Image changes update the existing service in place. Parent identifiers
and other immutable identity fields on all resources also use replacement
semantics.

Variable creation is create-only. If the same key already exists in Railway,
creation fails instead of overwriting it. Import the existing variable first,
then let Pulumi update it.

## Import

```bash
pulumi import railway:index:Project project <project-id>
pulumi import railway:index:Environment staging <project-id>/<environment-id>
pulumi import railway:index:Service web <service-id>/<environment-id>
pulumi import railway:index:Variable nodeEnv <project-id>/<environment-id>/<service-id>/<key>
pulumi import railway:index:Variable shared <project-id>/<environment-id>//<key>
pulumi import railway:index:CustomDomain api <domain-id>/<project-id>/<environment-id>/<service-id>
pulumi import railway:index:Volume data <volume-id>/<project-id>
# If a volume has more than one attachment, qualify the import:
pulumi import railway:index:Volume data <volume-id>/<project-id>/<environment-id>/<service-id>
pulumi import railway:index:Bucket uploads <project-id>/<environment-id>/<bucket-id>
```

## Development

Requirements:

- Go version declared in `provider/go.mod`
- Node.js 20 or newer
- Pulumi CLI
- GNU Make, `jq`, and `golangci-lint` v2

```bash
make build          # Build the provider binary
make schema         # Generate schema.json
make sdk            # Generate the TypeScript SDK
make nodejs-lock    # Refresh the committed SDK lockfile after metadata changes
make package-nodejs # Build and pack the npm artifact into dist/
make ci             # Tests, lint, codegen drift, SDK package, and example typecheck
make release-snapshot VERSION=0.1.0 # Verify all release archives locally
```

Generated schema and SDK metadata come directly from the provider. They use the
root `railway:index:*` module, npm package `@thegreataxios/pulumi-railway`, and the
GitHub release plugin source. No machine-local paths or platform-specific
post-processing are used.

## Releases

Push a semantic version tag such as `v0.1.0`. The release workflow:

1. Runs the complete validation suite.
2. Builds provider archives for Linux, macOS, and Windows on amd64 and arm64.
3. Publishes the archives and checksums to a GitHub release.
4. Publishes the matching public npm package with provenance.

Configure the `NPM_TOKEN` GitHub Actions secret before creating the first tag.
The npm version and GitHub tag must match.

Prereleases such as `v0.1.0-alpha.1` are published to npm with the `next`
dist-tag. Stable semantic versions use the default `latest` dist-tag.

See [CHANGELOG.md](./CHANGELOG.md) for user-visible changes.

## Railway API

- Endpoint: `https://backboard.railway.com/graphql/v2`
- Account/workspace auth: `Authorization: Bearer <token>`
- Project auth: `Project-Access-Token: <token>`
- Documentation: <https://docs.railway.com/integrations/api>

## License

MIT
