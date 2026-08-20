package pkg

import (
	"context"
	"fmt"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Project is a Railway project — the top-level container for services,
// environments, variables, and domains.
type Project struct{}

func (project *Project) Annotate(a infer.Annotator) {
	a.Describe(project, "A Railway project, the top-level container for services and environments.")
	a.SetToken("index", "Project")
	a.AddAlias("pkg", "Project")
}

type ProjectArgs struct {
	// Name of the project.
	Name string `pulumi:"name"`
	// Optional description.
	Description *string `pulumi:"description,optional"`
	// Optional workspace ID to create the project in.
	WorkspaceID *string `pulumi:"workspaceId,optional" provider:"replaceOnChanges"`
}

func (args *ProjectArgs) Annotate(a infer.Annotator) {
	a.Describe(args, "Inputs for creating or updating a Railway project.")
	a.Describe(&args.Name, "Name of the project.")
	a.Describe(&args.Description, "Optional project description.")
	a.Describe(&args.WorkspaceID, "Workspace ID in which to create the project. Projects without one are temporary and expire after 24 hours unless claimed.")
}

type ProjectState struct {
	ProjectArgs
	// Railway project ID (immutable).
	RailwayID string `pulumi:"railwayId"`
	// ID of the project's primary (default) environment, typically production.
	DefaultEnvironmentID string `pulumi:"defaultEnvironmentId"`
}

func (state *ProjectState) Annotate(a infer.Annotator) {
	a.Describe(state, "The observed state of a Railway project.")
	a.Describe(&state.Name, "Name of the project.")
	a.Describe(&state.Description, "Optional project description.")
	a.Describe(&state.WorkspaceID, "Workspace ID that contains the project.")
	a.Describe(&state.RailwayID, "Immutable Railway project ID.")
	a.Describe(&state.DefaultEnvironmentID, "ID of the project's primary environment, typically production.")
}

func (*Project) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[ProjectArgs], error) {
	response, err := checkInputs(ctx, req, func(inputs property.Map, input ProjectArgs) []provider.CheckFailure {
		return required(inputs, "name", input.Name)
	})
	if err == nil && response.Inputs.WorkspaceID == nil && !isUnknown(req.NewInputs, "workspaceId") {
		provider.GetLogger(ctx).Warning(
			"Railway projects created without workspaceId are temporary and expire after 24 hours unless claimed",
		)
	}
	return response, err
}

func (*Project) Create(
	ctx context.Context, req infer.CreateRequest[ProjectArgs],
) (infer.CreateResponse[ProjectState], error) {
	input := req.Inputs
	state := ProjectState{ProjectArgs: input}
	if req.DryRun {
		return infer.CreateResponse[ProjectState]{ID: req.Name, Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.CreateResponse[ProjectState]{}, err
	}

	var result struct {
		ProjectCreate struct {
			ID                   string `json:"id"`
			Name                 string `json:"name"`
			PrimaryEnvironmentID string `json:"primaryEnvironmentId"`
		} `json:"projectCreate"`
	}

	mutation := `mutation projectCreate($input: ProjectCreateInput!) {
  projectCreate(input: $input) {
    id name primaryEnvironmentId
  }
}`

	createInput := map[string]interface{}{"name": input.Name}
	if input.Description != nil {
		createInput["description"] = *input.Description
	}
	if input.WorkspaceID != nil {
		createInput["workspaceId"] = *input.WorkspaceID
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": createInput}, &result); err != nil {
		return infer.CreateResponse[ProjectState]{}, fmt.Errorf("create project: %w", err)
	}
	if err := requireCreatedID("project", result.ProjectCreate.ID); err != nil {
		return infer.CreateResponse[ProjectState]{}, err
	}

	state.RailwayID = result.ProjectCreate.ID
	state.DefaultEnvironmentID = result.ProjectCreate.PrimaryEnvironmentID
	return infer.CreateResponse[ProjectState]{ID: result.ProjectCreate.ID, Output: state}, nil
}

func (*Project) Read(
	ctx context.Context, req infer.ReadRequest[ProjectArgs, ProjectState],
) (infer.ReadResponse[ProjectArgs, ProjectState], error) {
	state := req.State
	client, err := getClient(ctx)
	if err != nil {
		return infer.ReadResponse[ProjectArgs, ProjectState]{}, err
	}

	var result struct {
		Project struct {
			ID                   string  `json:"id"`
			Name                 string  `json:"name"`
			Description          string  `json:"description"`
			PrimaryEnvironmentID string  `json:"primaryEnvironmentId"`
			WorkspaceID          *string `json:"workspaceId"`
		} `json:"project"`
	}

	query := `query project($id: String!) {
  project(id: $id) { id name description primaryEnvironmentId workspaceId }
}`

	if err := client.query(ctx, query, map[string]interface{}{"id": req.ID}, &result); err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[ProjectArgs, ProjectState]{}, nil
		}
		return infer.ReadResponse[ProjectArgs, ProjectState]{}, fmt.Errorf("read project: %w", err)
	}
	if result.Project.ID == "" {
		return infer.ReadResponse[ProjectArgs, ProjectState]{}, nil
	}

	state.RailwayID = result.Project.ID
	state.Name = result.Project.Name
	state.WorkspaceID = result.Project.WorkspaceID
	state.DefaultEnvironmentID = result.Project.PrimaryEnvironmentID
	if strings.TrimSpace(result.Project.Description) == "" {
		state.Description = nil
	} else {
		state.Description = &result.Project.Description
	}
	inputs := state.ProjectArgs
	return infer.ReadResponse[ProjectArgs, ProjectState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

func (*Project) Update(
	ctx context.Context, req infer.UpdateRequest[ProjectArgs, ProjectState],
) (infer.UpdateResponse[ProjectState], error) {
	input := req.Inputs
	state := req.State
	if req.DryRun {
		state.ProjectArgs = input
		return infer.UpdateResponse[ProjectState]{Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.UpdateResponse[ProjectState]{}, err
	}

	mutation := `mutation projectUpdate($id: String!, $input: ProjectUpdateInput!) {
  projectUpdate(id: $id, input: $input) { id name }
}`

	updateInput := map[string]interface{}{"name": input.Name}
	if input.Description != nil {
		updateInput["description"] = *input.Description
	} else if state.Description != nil {
		updateInput["description"] = nil
	}
	vars := map[string]interface{}{"id": req.ID, "input": updateInput}

	if err := client.mutate(ctx, mutation, vars, nil); err != nil {
		return infer.UpdateResponse[ProjectState]{}, fmt.Errorf("update project: %w", err)
	}

	state.ProjectArgs = input
	return infer.UpdateResponse[ProjectState]{Output: state}, nil
}

func (*Project) Delete(
	ctx context.Context, req infer.DeleteRequest[ProjectState],
) (infer.DeleteResponse, error) {
	client, err := getClient(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}

	mutation := `mutation projectDelete($id: String!) { projectDelete(id: $id) }`

	if err := client.mutate(ctx, mutation, map[string]interface{}{"id": req.ID}, nil); err != nil {
		if isNotFound(err) {
			return infer.DeleteResponse{}, nil
		}
		return infer.DeleteResponse{}, fmt.Errorf("delete project: %w", err)
	}

	return infer.DeleteResponse{}, nil
}

// --- helpers ---

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
