package pkg

import (
	"context"
	"fmt"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Environment is a Railway environment inside a project (for example
// "production" or "staging"). Projects always have a default environment;
// this resource manages additional named environments.
type Environment struct{}

func (environment *Environment) Annotate(a infer.Annotator) {
	a.Describe(environment, "A Railway environment inside a project.")
	a.SetToken("index", "Environment")
	a.AddAlias("pkg", "Environment")
}

type EnvironmentArgs struct {
	// Project ID that owns the environment.
	ProjectID string `pulumi:"projectId" provider:"replaceOnChanges"`
	// Environment name, unique within the project.
	Name string `pulumi:"name"`
}

func (args *EnvironmentArgs) Annotate(a infer.Annotator) {
	a.Describe(args, "Inputs for creating a Railway environment.")
	a.Describe(&args.ProjectID, "ID of the Railway project that owns the environment.")
	a.Describe(&args.Name, "Environment name. It must be unique within the project.")
}

type EnvironmentState struct {
	EnvironmentArgs
	// Railway environment ID (immutable).
	RailwayID string `pulumi:"railwayId"`
}

func (state *EnvironmentState) Annotate(a infer.Annotator) {
	a.Describe(state, "The observed state of a Railway environment.")
	a.Describe(&state.ProjectID, "ID of the Railway project that owns the environment.")
	a.Describe(&state.Name, "Environment name.")
	a.Describe(&state.RailwayID, "Immutable Railway environment ID.")
}

func (*Environment) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[EnvironmentArgs], error) {
	return checkInputs(ctx, req, func(inputs property.Map, input EnvironmentArgs) []provider.CheckFailure {
		return appendFailures(
			required(inputs, "projectId", input.ProjectID),
			required(inputs, "name", input.Name),
		)
	})
}

func (*Environment) Create(
	ctx context.Context, req infer.CreateRequest[EnvironmentArgs],
) (infer.CreateResponse[EnvironmentState], error) {
	input := req.Inputs
	state := EnvironmentState{EnvironmentArgs: input}
	if req.DryRun {
		return infer.CreateResponse[EnvironmentState]{ID: req.Name, Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.CreateResponse[EnvironmentState]{}, err
	}

	var createResult struct {
		EnvironmentCreate struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"environmentCreate"`
	}

	mutation := `mutation environmentCreate($input: EnvironmentCreateInput!) {
  environmentCreate(input: $input) { id name }
}`

	if err := client.mutate(ctx, mutation, map[string]interface{}{
		"input": map[string]interface{}{
			"projectId": input.ProjectID,
			"name":      input.Name,
		},
	}, &createResult); err != nil {
		return infer.CreateResponse[EnvironmentState]{}, fmt.Errorf("create environment: %w", err)
	}
	if err := requireCreatedID("environment", createResult.EnvironmentCreate.ID); err != nil {
		return infer.CreateResponse[EnvironmentState]{}, err
	}

	state.RailwayID = createResult.EnvironmentCreate.ID
	return infer.CreateResponse[EnvironmentState]{ID: state.RailwayID, Output: state}, nil
}

func (*Environment) Read(
	ctx context.Context, req infer.ReadRequest[EnvironmentArgs, EnvironmentState],
) (infer.ReadResponse[EnvironmentArgs, EnvironmentState], error) {
	inputs := req.Inputs
	state := req.State
	if strings.TrimSpace(inputs.ProjectID) == "" {
		parts := strings.SplitN(req.ID, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, fmt.Errorf(
				"environment import ID must be projectId/environmentId",
			)
		}
		inputs.ProjectID = parts[0]
		state.RailwayID = parts[1]
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, err
	}

	var result struct {
		Environment struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			ProjectID string `json:"projectId"`
		} `json:"environment"`
	}

	query := `query environment($id: String!) { environment(id: $id) { id name projectId } }`

	if err := client.query(ctx, query, map[string]interface{}{"id": state.RailwayID}, &result); err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, nil
		}
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, fmt.Errorf("read environment: %w", err)
	}
	if result.Environment.ID == "" {
		return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{}, nil
	}

	state.RailwayID = result.Environment.ID
	state.Name = result.Environment.Name
	state.ProjectID = result.Environment.ProjectID
	return infer.ReadResponse[EnvironmentArgs, EnvironmentState]{ID: state.RailwayID, Inputs: state.EnvironmentArgs, State: state}, nil
}

func (*Environment) Update(
	ctx context.Context, req infer.UpdateRequest[EnvironmentArgs, EnvironmentState],
) (infer.UpdateResponse[EnvironmentState], error) {
	input := req.Inputs
	state := req.State
	if req.DryRun {
		state.EnvironmentArgs = input
		return infer.UpdateResponse[EnvironmentState]{Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.UpdateResponse[EnvironmentState]{}, err
	}

	// Renaming is the only mutable field.
	if input.Name != state.Name {
		mutation := `mutation environmentRename($id: String!, $input: EnvironmentRenameInput!) {
  environmentRename(id: $id, input: $input) { id }
}`
		if err := client.mutate(ctx, mutation, map[string]interface{}{
			"id":    req.ID,
			"input": map[string]interface{}{"name": input.Name},
		}, nil); err != nil {
			return infer.UpdateResponse[EnvironmentState]{}, fmt.Errorf("rename environment: %w", err)
		}
	}

	state.EnvironmentArgs = input
	return infer.UpdateResponse[EnvironmentState]{Output: state}, nil
}

func (*Environment) Delete(
	ctx context.Context, req infer.DeleteRequest[EnvironmentState],
) (infer.DeleteResponse, error) {
	client, err := getClient(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}

	mutation := `mutation environmentDelete($id: String!) { environmentDelete(id: $id) }`

	if err := client.mutate(ctx, mutation, map[string]interface{}{"id": req.ID}, nil); err != nil {
		if isNotFound(err) {
			return infer.DeleteResponse{}, nil
		}
		return infer.DeleteResponse{}, fmt.Errorf("delete environment: %w", err)
	}

	return infer.DeleteResponse{}, nil
}
