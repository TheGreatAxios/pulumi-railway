package pkg

import (
	"context"
	"fmt"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Service is a Railway service. It combines the Railway "service" (name,
// source) with its per-environment "service instance" config (build, start,
// healthcheck, region, replicas) into a single ergonomic Pulumi resource.
type Service struct{}

func (service *Service) Annotate(a infer.Annotator) {
	a.Describe(service, "A Railway service and its configuration in one environment.")
	a.SetToken("index", "Service")
	a.AddAlias("pkg", "Service")
}

type ServiceArgs struct {
	// Project ID this service belongs to.
	ProjectID string `pulumi:"projectId" provider:"replaceOnChanges"`
	// Environment ID for the service instance config.
	EnvironmentID string `pulumi:"environmentId" provider:"replaceOnChanges"`
	// Service name (as shown in Railway dashboard).
	Name string `pulumi:"name"`
	// Docker image source, e.g. "redis:7-alpine". Mutually exclusive with repo.
	Image *string `pulumi:"image,optional" provider:"replaceOnChanges"`
	// GitHub repo source, e.g. "owner/repo". Mutually exclusive with image.
	Repo *string `pulumi:"repo,optional" provider:"replaceOnChanges"`
	// GitHub branch (only with repo).
	Branch *string `pulumi:"branch,optional" provider:"replaceOnChanges"`
	// Root directory for the build context.
	RootDirectory *string `pulumi:"rootDirectory,optional"`
	// Build command.
	BuildCommand *string `pulumi:"buildCommand,optional"`
	// Start command.
	StartCommand *string `pulumi:"startCommand,optional"`
	// Healthcheck path, e.g. "/health".
	HealthcheckPath *string `pulumi:"healthcheckPath,optional"`
	// Region for the service instance.
	Region *string `pulumi:"region,optional"`
	// Number of replicas.
	NumReplicas *int `pulumi:"numReplicas,optional"`
}

func (args *ServiceArgs) Annotate(a infer.Annotator) {
	a.Describe(args, "Inputs for creating or updating a Railway service.")
	a.Describe(&args.ProjectID, "ID of the Railway project that owns the service.")
	a.Describe(&args.EnvironmentID, "ID of the environment whose service instance is configured.")
	a.Describe(&args.Name, "Service name shown in Railway.")
	a.Describe(&args.Image, "Docker image source, for example redis:7-alpine. Mutually exclusive with repo.")
	a.Describe(&args.Repo, "GitHub repository source in owner/repository form. Mutually exclusive with image.")
	a.Describe(&args.Branch, "GitHub branch to deploy. Valid only when repo is set.")
	a.Describe(&args.RootDirectory, "Root directory used as the build context.")
	a.Describe(&args.BuildCommand, "Command Railway runs to build the service.")
	a.Describe(&args.StartCommand, "Command Railway runs to start the service.")
	a.Describe(&args.HealthcheckPath, "HTTP path Railway uses for health checks.")
	a.Describe(&args.Region, "Railway region for the service instance.")
	a.Describe(&args.NumReplicas, "Number of service replicas. Must be at least one.")
}

type ServiceState struct {
	ServiceArgs
	// Railway service ID (immutable).
	RailwayID string `pulumi:"railwayId"`
	// Service instance ID.
	InstanceID string `pulumi:"instanceId"`
}

func (state *ServiceState) Annotate(a infer.Annotator) {
	a.Describe(state, "The observed state of a Railway service and environment-specific instance.")
	a.Describe(&state.ProjectID, "ID of the Railway project that owns the service.")
	a.Describe(&state.EnvironmentID, "ID of the configured environment.")
	a.Describe(&state.Name, "Service name shown in Railway.")
	a.Describe(&state.Image, "Docker image source.")
	a.Describe(&state.Repo, "GitHub repository source.")
	a.Describe(&state.Branch, "GitHub branch deployed by the service.")
	a.Describe(&state.RootDirectory, "Root directory used as the build context.")
	a.Describe(&state.BuildCommand, "Command Railway runs to build the service.")
	a.Describe(&state.StartCommand, "Command Railway runs to start the service.")
	a.Describe(&state.HealthcheckPath, "HTTP path Railway uses for health checks.")
	a.Describe(&state.Region, "Railway region for the service instance.")
	a.Describe(&state.NumReplicas, "Number of service replicas.")
	a.Describe(&state.RailwayID, "Immutable Railway service ID.")
	a.Describe(&state.InstanceID, "Railway service instance ID for the configured environment.")
}

func (*Service) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[ServiceArgs], error) {
	return checkInputs(ctx, req, func(inputs property.Map, input ServiceArgs) []provider.CheckFailure {
		failures := appendFailures(
			required(inputs, "projectId", input.ProjectID),
			required(inputs, "environmentId", input.EnvironmentID),
			required(inputs, "name", input.Name),
		)
		if input.Image != nil && !isUnknown(inputs, "image") && strings.TrimSpace(*input.Image) == "" {
			failures = append(failures, provider.CheckFailure{
				Property: "image", Reason: "image must not be empty when provided",
			})
		}
		if input.Repo != nil && !isUnknown(inputs, "repo") && strings.TrimSpace(*input.Repo) == "" {
			failures = append(failures, provider.CheckFailure{
				Property: "repo", Reason: "repo must not be empty when provided",
			})
		}
		if input.Image != nil && input.Repo != nil &&
			!isUnknown(inputs, "image") && !isUnknown(inputs, "repo") {
			failures = append(failures, provider.CheckFailure{
				Property: "image", Reason: "image and repo are mutually exclusive",
			})
		}
		if input.Branch != nil && input.Repo == nil &&
			!isUnknown(inputs, "branch") && !isUnknown(inputs, "repo") {
			failures = append(failures, provider.CheckFailure{
				Property: "branch", Reason: "branch requires repo",
			})
		}
		if input.NumReplicas != nil && !isUnknown(inputs, "numReplicas") && *input.NumReplicas < 1 {
			failures = append(failures, provider.CheckFailure{
				Property: "numReplicas", Reason: "numReplicas must be at least 1",
			})
		}
		return failures
	})
}

func (*Service) Create(
	ctx context.Context, req infer.CreateRequest[ServiceArgs],
) (infer.CreateResponse[ServiceState], error) {
	input := req.Inputs
	state := ServiceState{ServiceArgs: input}
	if req.DryRun {
		return infer.CreateResponse[ServiceState]{ID: req.Name, Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.CreateResponse[ServiceState]{}, err
	}

	// 1. Create the service
	var createResult struct {
		ServiceCreate struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"serviceCreate"`
	}

	mutation := `mutation serviceCreate($input: ServiceCreateInput!) {
  serviceCreate(input: $input) { id name }
}`

	createInput := map[string]interface{}{
		"projectId": input.ProjectID,
		"name":      input.Name,
	}

	if input.Image != nil {
		createInput["source"] = map[string]interface{}{"image": *input.Image}
	} else if input.Repo != nil {
		createInput["source"] = map[string]interface{}{"repo": *input.Repo}
		if input.Branch != nil {
			createInput["branch"] = *input.Branch
		}
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": createInput}, &createResult); err != nil {
		return infer.CreateResponse[ServiceState]{}, fmt.Errorf("create service: %w", err)
	}
	if err := requireCreatedID("service", createResult.ServiceCreate.ID); err != nil {
		return infer.CreateResponse[ServiceState]{}, err
	}

	serviceID := createResult.ServiceCreate.ID
	state.RailwayID = serviceID

	// 2. Update the service instance (per-environment config)
	if err := updateServiceInstance(ctx, client, serviceID, input.EnvironmentID, ServiceArgs{}, input, false); err != nil {
		return infer.CreateResponse[ServiceState]{ID: serviceID, Output: state}, infer.ResourceInitFailedError{
			Reasons: []string{fmt.Sprintf("configure service instance: %v", err)},
		}
	}

	// 3. Read the service instance to get its ID
	var instanceResult struct {
		ServiceInstance struct {
			ID string `json:"id"`
		} `json:"serviceInstance"`
	}

	instanceQuery := `query serviceInstance($serviceId: String!, $environmentId: String!) {
  serviceInstance(serviceId: $serviceId, environmentId: $environmentId) { id }
}`

	if err := client.query(ctx, instanceQuery, map[string]interface{}{
		"serviceId":     serviceID,
		"environmentId": input.EnvironmentID,
	}, &instanceResult); err != nil {
		return infer.CreateResponse[ServiceState]{ID: serviceID, Output: state}, infer.ResourceInitFailedError{
			Reasons: []string{fmt.Sprintf("read created service instance: %v", err)},
		}
	}
	if instanceResult.ServiceInstance.ID == "" {
		return infer.CreateResponse[ServiceState]{ID: serviceID, Output: state}, infer.ResourceInitFailedError{
			Reasons: []string{"Railway did not return the created service instance"},
		}
	}
	state.InstanceID = instanceResult.ServiceInstance.ID

	return infer.CreateResponse[ServiceState]{ID: serviceID, Output: state}, nil
}

func (*Service) Read(
	ctx context.Context, req infer.ReadRequest[ServiceArgs, ServiceState],
) (infer.ReadResponse[ServiceArgs, ServiceState], error) {
	state := req.State
	client, err := getClient(ctx)
	if err != nil {
		return infer.ReadResponse[ServiceArgs, ServiceState]{}, err
	}
	serviceID := req.ID
	inputs := req.Inputs
	if strings.Contains(req.ID, "/") {
		parts := strings.SplitN(req.ID, "/", 2)
		serviceID = parts[0]
		if inputs.EnvironmentID == "" {
			inputs.EnvironmentID = parts[1]
		}
	}
	if strings.TrimSpace(inputs.EnvironmentID) == "" {
		return infer.ReadResponse[ServiceArgs, ServiceState]{}, fmt.Errorf(
			"service import ID must be serviceId/environmentId",
		)
	}

	var result struct {
		Service struct {
			ID        string  `json:"id"`
			Name      string  `json:"name"`
			ProjectID string  `json:"projectId"`
			Branch    *string `json:"branch"`
		} `json:"service"`
	}

	query := `query service($id: String!) { service(id: $id) { id name projectId branch } }`

	if err := client.query(ctx, query, map[string]interface{}{"id": serviceID}, &result); err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[ServiceArgs, ServiceState]{}, nil
		}
		return infer.ReadResponse[ServiceArgs, ServiceState]{}, fmt.Errorf("read service: %w", err)
	}
	if result.Service.ID == "" {
		return infer.ReadResponse[ServiceArgs, ServiceState]{}, nil
	}

	state.RailwayID = result.Service.ID
	state.Name = result.Service.Name
	state.ProjectID = result.Service.ProjectID
	state.EnvironmentID = inputs.EnvironmentID
	state.Branch = result.Service.Branch

	// Read service instance
	var instanceResult struct {
		ServiceInstance struct {
			ID              string  `json:"id"`
			StartCommand    *string `json:"startCommand"`
			BuildCommand    *string `json:"buildCommand"`
			RootDirectory   *string `json:"rootDirectory"`
			HealthcheckPath *string `json:"healthcheckPath"`
			Region          *string `json:"region"`
			NumReplicas     *int    `json:"numReplicas"`
			Source          *struct {
				Image *string `json:"image"`
				Repo  *string `json:"repo"`
			} `json:"source"`
		} `json:"serviceInstance"`
	}

	instanceQuery := `query serviceInstance($serviceId: String!, $environmentId: String!) {
  serviceInstance(serviceId: $serviceId, environmentId: $environmentId) {
    id startCommand buildCommand rootDirectory healthcheckPath region numReplicas
    source { image repo }
  }
}`

	if err := client.query(ctx, instanceQuery, map[string]interface{}{
		"serviceId":     serviceID,
		"environmentId": inputs.EnvironmentID,
	}, &instanceResult); err != nil {
		return infer.ReadResponse[ServiceArgs, ServiceState]{}, fmt.Errorf("read service instance: %w", err)
	}
	if instanceResult.ServiceInstance.ID == "" {
		return infer.ReadResponse[ServiceArgs, ServiceState]{}, fmt.Errorf(
			"service %s has no instance in environment %s", serviceID, inputs.EnvironmentID,
		)
	}
	state.InstanceID = instanceResult.ServiceInstance.ID
	state.StartCommand = instanceResult.ServiceInstance.StartCommand
	state.BuildCommand = instanceResult.ServiceInstance.BuildCommand
	state.RootDirectory = instanceResult.ServiceInstance.RootDirectory
	state.HealthcheckPath = instanceResult.ServiceInstance.HealthcheckPath
	state.Region = instanceResult.ServiceInstance.Region
	state.NumReplicas = instanceResult.ServiceInstance.NumReplicas
	if source := instanceResult.ServiceInstance.Source; source != nil {
		state.Image = source.Image
		state.Repo = source.Repo
	}

	return infer.ReadResponse[ServiceArgs, ServiceState]{ID: serviceID, Inputs: state.ServiceArgs, State: state}, nil
}

func (*Service) Update(
	ctx context.Context, req infer.UpdateRequest[ServiceArgs, ServiceState],
) (infer.UpdateResponse[ServiceState], error) {
	input := req.Inputs
	state := req.State
	if req.DryRun {
		state.ServiceArgs = input
		return infer.UpdateResponse[ServiceState]{Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.UpdateResponse[ServiceState]{}, err
	}

	// Update service name if changed
	if input.Name != state.Name {
		mutation := `mutation serviceUpdate($id: String!, $input: ServiceUpdateInput!) {
  serviceUpdate(id: $id, input: $input) { id name }
}`
		vars := map[string]interface{}{
			"id":    req.ID,
			"input": map[string]interface{}{"name": input.Name},
		}
		if err := client.mutate(ctx, mutation, vars, nil); err != nil {
			return infer.UpdateResponse[ServiceState]{}, fmt.Errorf("update service: %w", err)
		}
	}

	// Update service instance config
	if err := updateServiceInstance(ctx, client, req.ID, input.EnvironmentID, state.ServiceArgs, input, true); err != nil {
		return infer.UpdateResponse[ServiceState]{}, fmt.Errorf("update service instance: %w", err)
	}

	state.ServiceArgs = input
	return infer.UpdateResponse[ServiceState]{Output: state}, nil
}

func (*Service) Delete(
	ctx context.Context, req infer.DeleteRequest[ServiceState],
) (infer.DeleteResponse, error) {
	client, err := getClient(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}

	mutation := `mutation serviceDelete($id: String!) { serviceDelete(id: $id) }`

	if err := client.mutate(ctx, mutation, map[string]interface{}{"id": req.ID}, nil); err != nil {
		if isNotFound(err) {
			return infer.DeleteResponse{}, nil
		}
		return infer.DeleteResponse{}, fmt.Errorf("delete service: %w", err)
	}

	return infer.DeleteResponse{}, nil
}

// updateServiceInstance sends a serviceInstanceUpdate mutation with the
// per-environment config from the ServiceArgs.
func updateServiceInstance(
	ctx context.Context,
	client *Client,
	serviceID string,
	environmentID string,
	old ServiceArgs,
	input ServiceArgs,
	includeClears bool,
) error {
	instanceInput := map[string]interface{}{}
	addOptionalString(instanceInput, "buildCommand", old.BuildCommand, input.BuildCommand, includeClears)
	addOptionalString(instanceInput, "startCommand", old.StartCommand, input.StartCommand, includeClears)
	addOptionalString(instanceInput, "rootDirectory", old.RootDirectory, input.RootDirectory, includeClears)
	addOptionalString(instanceInput, "healthcheckPath", old.HealthcheckPath, input.HealthcheckPath, includeClears)
	addOptionalString(instanceInput, "region", old.Region, input.Region, includeClears)
	addOptionalInt(instanceInput, "numReplicas", old.NumReplicas, input.NumReplicas, includeClears)

	if len(instanceInput) == 0 {
		return nil
	}

	mutation := `mutation serviceInstanceUpdate($serviceId: String!, $environmentId: String!, $input: ServiceInstanceUpdateInput!) {
  serviceInstanceUpdate(serviceId: $serviceId, environmentId: $environmentId, input: $input)
}`

	vars := map[string]interface{}{
		"serviceId":     serviceID,
		"environmentId": environmentID,
		"input":         instanceInput,
	}

	return client.mutate(ctx, mutation, vars, nil)
}

func addOptionalString(
	target map[string]interface{}, name string, old, current *string, includeClears bool,
) {
	if !includeClears {
		if current != nil {
			target[name] = *current
		}
		return
	}
	if equalPointers(old, current) {
		return
	}
	if current == nil {
		target[name] = nil
		return
	}
	target[name] = *current
}

func addOptionalInt(
	target map[string]interface{}, name string, old, current *int, includeClears bool,
) {
	if !includeClears {
		if current != nil {
			target[name] = *current
		}
		return
	}
	if equalPointers(old, current) {
		return
	}
	if current == nil {
		target[name] = nil
		return
	}
	target[name] = *current
}

func equalPointers[T comparable](left, right *T) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
