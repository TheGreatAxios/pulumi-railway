import * as pulumi from "@pulumi/pulumi";
import * as railway from "@thegreataxios/pulumi-railway";

// Target workspace for the project, set per stack:
//   pulumi config set workspaceId <workspace-id>
// Without it, Railway creates a temporary public project that expires
// after 24 hours unless claimed.
const config = new pulumi.Config();
const workspaceId = config.get("workspaceId");

// Create a Railway project
const project = new railway.Project("my-project", {
  name: "my-railway-project",
  description: "Created via Pulumi",
  workspaceId,
});

// Create a web service from a Docker image
const web = new railway.Service("web", {
  projectId: project.id,
  environmentId: project.defaultEnvironmentId,
  name: "web",
  image: "node:20-alpine",
  startCommand: "node server.js",
  healthcheckPath: "/health",
  numReplicas: 1,
});

// Set an environment variable on the web service
const nodeEnv = new railway.Variable("node-env", {
  projectId: project.id,
  environmentId: project.defaultEnvironmentId,
  serviceId: web.id,
  key: "NODE_ENV",
  value: "production",
});

// Add a custom domain
const domain = new railway.CustomDomain("api-domain", {
  projectId: project.id,
  environmentId: project.defaultEnvironmentId,
  serviceId: web.id,
  domain: "api.example.com",
});

// Export the service ID and domain verification token
export const projectId = project.id;
export const serviceId = web.id;
export const variableUrn = nodeEnv.urn;
export const verificationToken = domain.verificationToken;
