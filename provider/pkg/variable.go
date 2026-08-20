package pkg

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Variable is an environment variable on a Railway service (or shared on
// the environment if serviceId is empty).
type Variable struct{}

type VariableArgs struct {
	// Project ID.
	ProjectID string `pulumi:"projectId" provider:"replaceOnChanges"`
	// Environment ID.
	EnvironmentID string `pulumi:"environmentId" provider:"replaceOnChanges"`
	// Service ID. Omit for a shared environment variable.
	ServiceID *string `pulumi:"serviceId,optional" provider:"replaceOnChanges"`
	// Variable name (key).
	Key string `pulumi:"key" provider:"replaceOnChanges"`
	// Variable value.
	Value string `pulumi:"value" provider:"secret"`
}

var variableKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (variable *Variable) Annotate(a infer.Annotator) {
	a.Describe(variable, "A service-scoped or environment-shared Railway variable.")
	a.SetToken("index", "Variable")
	a.AddAlias("pkg", "Variable")
}

func (args *VariableArgs) Annotate(a infer.Annotator) {
	a.Describe(args, "Inputs for managing a Railway variable.")
	a.Describe(&args.ProjectID, "ID of the Railway project that owns the variable.")
	a.Describe(&args.EnvironmentID, "ID of the Railway environment that owns the variable.")
	a.Describe(&args.ServiceID, "Optional service ID. Omit it for an environment-shared variable.")
	a.Describe(&args.Key, "Variable name. It must be a valid environment variable identifier.")
	a.Describe(&args.Value, "Secret variable value.")
}

type VariableState struct {
	VariableArgs
	// Composite ID: projectId/environmentId/serviceId/key.
	RailwayID string `pulumi:"railwayId"`
}

func (state *VariableState) Annotate(a infer.Annotator) {
	a.Describe(state, "The observed state of a Railway variable.")
	a.Describe(&state.ProjectID, "ID of the Railway project that owns the variable.")
	a.Describe(&state.EnvironmentID, "ID of the Railway environment that owns the variable.")
	a.Describe(&state.ServiceID, "Optional service ID, unset for an environment-shared variable.")
	a.Describe(&state.Key, "Variable name.")
	a.Describe(&state.Value, "Secret variable value.")
	a.Describe(&state.RailwayID, "Composite identifier in projectId/environmentId/serviceId/key form.")
}

func (*Variable) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[VariableArgs], error) {
	return checkInputs(ctx, req, func(inputs property.Map, input VariableArgs) []provider.CheckFailure {
		failures := appendFailures(
			required(inputs, "projectId", input.ProjectID),
			required(inputs, "environmentId", input.EnvironmentID),
			required(inputs, "key", input.Key),
		)
		if input.ServiceID != nil {
			failures = append(failures, required(inputs, "serviceId", *input.ServiceID)...)
		}
		if input.Key != "" && !variableKeyPattern.MatchString(input.Key) {
			failures = append(failures, provider.CheckFailure{
				Property: "key",
				Reason:   "key must start with a letter or underscore and contain only letters, numbers, and underscores",
			})
		}
		return failures
	})
}

func (*Variable) Create(
	ctx context.Context, req infer.CreateRequest[VariableArgs],
) (infer.CreateResponse[VariableState], error) {
	input := req.Inputs
	state := VariableState{VariableArgs: input}
	if req.DryRun {
		return infer.CreateResponse[VariableState]{ID: req.Name, Output: state}, nil
	}

	client := getClient(ctx)
	compositeID := fmt.Sprintf("%s/%s/%s/%s", input.ProjectID, input.EnvironmentID, deref(input.ServiceID), input.Key)
	existing, err := readVariables(ctx, client, input)
	if err != nil {
		return infer.CreateResponse[VariableState]{}, fmt.Errorf("check whether variable exists: %w", err)
	}
	if _, exists := existing[input.Key]; exists {
		return infer.CreateResponse[VariableState]{}, fmt.Errorf(
			"variable %q already exists; import it with %q instead of creating it",
			input.Key, compositeID,
		)
	}

	mutation := `mutation variableUpsert($input: VariableUpsertInput!) {
  variableUpsert(input: $input)
}`

	upsertInput := map[string]interface{}{
		"projectId":     input.ProjectID,
		"environmentId": input.EnvironmentID,
		"name":          input.Key,
		"value":         input.Value,
	}
	if input.ServiceID != nil {
		upsertInput["serviceId"] = *input.ServiceID
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": upsertInput}, nil); err != nil {
		return infer.CreateResponse[VariableState]{}, fmt.Errorf("create variable: %w", err)
	}

	state.RailwayID = compositeID
	return infer.CreateResponse[VariableState]{ID: compositeID, Output: state}, nil
}

func (*Variable) Read(
	ctx context.Context, req infer.ReadRequest[VariableArgs, VariableState],
) (infer.ReadResponse[VariableArgs, VariableState], error) {
	inputs := req.Inputs
	if strings.TrimSpace(inputs.ProjectID) == "" {
		var err error
		inputs, err = parseVariableID(req.ID)
		if err != nil {
			return infer.ReadResponse[VariableArgs, VariableState]{}, err
		}
	}

	client := getClient(ctx)
	variables, err := readVariables(ctx, client, inputs)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[VariableArgs, VariableState]{}, nil
		}
		return infer.ReadResponse[VariableArgs, VariableState]{}, fmt.Errorf("read variables: %w", err)
	}
	value, exists := variables[inputs.Key]
	if !exists {
		return infer.ReadResponse[VariableArgs, VariableState]{}, nil
	}
	inputs.Value = value
	state := VariableState{VariableArgs: inputs, RailwayID: req.ID}
	return infer.ReadResponse[VariableArgs, VariableState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

func (*Variable) Update(
	ctx context.Context, req infer.UpdateRequest[VariableArgs, VariableState],
) (infer.UpdateResponse[VariableState], error) {
	input := req.Inputs
	state := req.State
	if req.DryRun {
		state.VariableArgs = input
		return infer.UpdateResponse[VariableState]{Output: state}, nil
	}

	client := getClient(ctx)

	mutation := `mutation variableUpsert($input: VariableUpsertInput!) {
  variableUpsert(input: $input)
}`

	upsertInput := map[string]interface{}{
		"projectId":     input.ProjectID,
		"environmentId": input.EnvironmentID,
		"name":          input.Key,
		"value":         input.Value,
	}
	if input.ServiceID != nil {
		upsertInput["serviceId"] = *input.ServiceID
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": upsertInput}, nil); err != nil {
		return infer.UpdateResponse[VariableState]{}, fmt.Errorf("update variable: %w", err)
	}

	state.VariableArgs = input
	return infer.UpdateResponse[VariableState]{Output: state}, nil
}

func (*Variable) Delete(
	ctx context.Context, req infer.DeleteRequest[VariableState],
) (infer.DeleteResponse, error) {
	client := getClient(ctx)

	mutation := `mutation variableDelete($input: VariableDeleteInput!) {
  variableDelete(input: $input)
}`

	deleteInput := map[string]interface{}{
		"projectId":     req.State.ProjectID,
		"environmentId": req.State.EnvironmentID,
		"name":          req.State.Key,
	}
	if req.State.ServiceID != nil {
		deleteInput["serviceId"] = *req.State.ServiceID
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": deleteInput}, nil); err != nil {
		if isNotFound(err) {
			return infer.DeleteResponse{}, nil
		}
		return infer.DeleteResponse{}, fmt.Errorf("delete variable: %w", err)
	}

	return infer.DeleteResponse{}, nil
}

func readVariables(ctx context.Context, client *Client, inputs VariableArgs) (map[string]string, error) {
	var result struct {
		Variables map[string]string `json:"variables"`
	}
	query := `query variables($projectId: String!, $environmentId: String!, $serviceId: String) {
  variables(projectId: $projectId, environmentId: $environmentId, serviceId: $serviceId)
}`
	vars := map[string]interface{}{
		"projectId":     inputs.ProjectID,
		"environmentId": inputs.EnvironmentID,
	}
	if inputs.ServiceID != nil {
		vars["serviceId"] = *inputs.ServiceID
	}
	if err := client.query(ctx, query, vars, &result); err != nil {
		return nil, err
	}
	return result.Variables, nil
}

func parseVariableID(id string) (VariableArgs, error) {
	parts := strings.SplitN(id, "/", 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return VariableArgs{}, fmt.Errorf(
			"invalid variable import ID %q; expected projectId/environmentId/serviceId/key (serviceId may be empty)",
			id,
		)
	}
	inputs := VariableArgs{ProjectID: parts[0], EnvironmentID: parts[1], Key: parts[3]}
	if parts[2] != "" {
		inputs.ServiceID = &parts[2]
	}
	return inputs, nil
}
