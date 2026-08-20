package pkg

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Volume is a persistent volume attached to a Railway service.
type Volume struct{}

func (volume *Volume) Annotate(a infer.Annotator) {
	a.Describe(volume, "A persistent Railway volume attached to a service instance.")
	a.SetToken("index", "Volume")
	a.AddAlias("pkg", "Volume")
}

type VolumeArgs struct {
	// Project ID.
	ProjectID string `pulumi:"projectId" provider:"replaceOnChanges"`
	// Environment ID the volume instance lives in.
	EnvironmentID string `pulumi:"environmentId" provider:"replaceOnChanges"`
	// Service ID to attach the volume to.
	ServiceID string `pulumi:"serviceId" provider:"replaceOnChanges"`
	// Mount path inside the container.
	MountPath string `pulumi:"mountPath"`
	// Volume name.
	Name *string `pulumi:"name,optional"`
}

func (args *VolumeArgs) Annotate(a infer.Annotator) {
	a.Describe(args, "Inputs for managing a Railway persistent volume.")
	a.Describe(&args.ProjectID, "ID of the Railway project that owns the volume.")
	a.Describe(&args.EnvironmentID, "ID of the Railway environment where the volume is attached.")
	a.Describe(&args.ServiceID, "ID of the Railway service where the volume is attached.")
	a.Describe(&args.MountPath, "Absolute normalized mount path inside the service container.")
	a.Describe(&args.Name, "Optional display name for the volume.")
}

type VolumeState struct {
	VolumeArgs
	// Railway volume ID.
	RailwayID string `pulumi:"railwayId"`
}

func (state *VolumeState) Annotate(a infer.Annotator) {
	a.Describe(state, "The observed state of a Railway persistent volume.")
	a.Describe(&state.ProjectID, "ID of the Railway project that owns the volume.")
	a.Describe(&state.EnvironmentID, "ID of the Railway environment where the volume is attached.")
	a.Describe(&state.ServiceID, "ID of the Railway service where the volume is attached.")
	a.Describe(&state.MountPath, "Absolute mount path inside the service container.")
	a.Describe(&state.Name, "Optional display name for the volume.")
	a.Describe(&state.RailwayID, "Railway volume ID.")
}

func (*Volume) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[VolumeArgs], error) {
	return checkInputs(ctx, req, func(inputs property.Map, input VolumeArgs) []provider.CheckFailure {
		failures := appendFailures(
			required(inputs, "projectId", input.ProjectID),
			required(inputs, "environmentId", input.EnvironmentID),
			required(inputs, "serviceId", input.ServiceID),
			required(inputs, "mountPath", input.MountPath),
		)
		if input.MountPath != "" && (!path.IsAbs(input.MountPath) || path.Clean(input.MountPath) != input.MountPath) {
			failures = append(failures, provider.CheckFailure{
				Property: "mountPath", Reason: "mountPath must be an absolute, normalized container path",
			})
		}
		return failures
	})
}

func (*Volume) Create(
	ctx context.Context, req infer.CreateRequest[VolumeArgs],
) (infer.CreateResponse[VolumeState], error) {
	input := req.Inputs
	state := VolumeState{VolumeArgs: input}
	if req.DryRun {
		return infer.CreateResponse[VolumeState]{ID: req.Name, Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.CreateResponse[VolumeState]{}, err
	}

	var result struct {
		VolumeCreate struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"volumeCreate"`
	}

	mutation := `mutation volumeCreate($input: VolumeCreateInput!) {
  volumeCreate(input: $input) { id name }
}`

	// VolumeCreateInput has no name field; the name is applied afterwards.
	createInput := map[string]interface{}{
		"projectId":     input.ProjectID,
		"environmentId": input.EnvironmentID,
		"serviceId":     input.ServiceID,
		"mountPath":     input.MountPath,
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": createInput}, &result); err != nil {
		return infer.CreateResponse[VolumeState]{}, fmt.Errorf("create volume: %w", err)
	}
	if err := requireCreatedID("volume", result.VolumeCreate.ID); err != nil {
		return infer.CreateResponse[VolumeState]{}, err
	}

	state.RailwayID = result.VolumeCreate.ID

	if input.Name != nil {
		if err := updateVolumeName(ctx, client, result.VolumeCreate.ID, input.Name); err != nil {
			return infer.CreateResponse[VolumeState]{ID: result.VolumeCreate.ID, Output: state}, infer.ResourceInitFailedError{
				Reasons: []string{fmt.Sprintf("set volume name: %v", err)},
			}
		}
	}

	return infer.CreateResponse[VolumeState]{ID: result.VolumeCreate.ID, Output: state}, nil
}

func (*Volume) Read(
	ctx context.Context, req infer.ReadRequest[VolumeArgs, VolumeState],
) (infer.ReadResponse[VolumeArgs, VolumeState], error) {
	inputs := req.Inputs
	volumeID := req.ID
	// The Railway API has no volume(id) query, so imports must carry the
	// project ID: <volumeId>/<projectId>.
	if strings.TrimSpace(inputs.ProjectID) == "" {
		parts := strings.SplitN(req.ID, "/", 4)
		if (len(parts) != 2 && len(parts) != 4) || parts[0] == "" || parts[1] == "" {
			return infer.ReadResponse[VolumeArgs, VolumeState]{}, fmt.Errorf(
				"invalid volume import ID %q; expected volumeId/projectId or volumeId/projectId/environmentId/serviceId",
				req.ID,
			)
		}
		volumeID = parts[0]
		inputs.ProjectID = parts[1]
		if len(parts) == 4 {
			if parts[2] == "" || parts[3] == "" {
				return infer.ReadResponse[VolumeArgs, VolumeState]{}, fmt.Errorf(
					"invalid volume import ID %q; environmentId and serviceId must both be set",
					req.ID,
				)
			}
			inputs.EnvironmentID = parts[2]
			inputs.ServiceID = parts[3]
		}
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.ReadResponse[VolumeArgs, VolumeState]{}, err
	}
	volumeName, volumeFound, err := findProjectVolume(ctx, client, inputs.ProjectID, volumeID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[VolumeArgs, VolumeState]{}, nil
		}
		return infer.ReadResponse[VolumeArgs, VolumeState]{}, fmt.Errorf("read project volumes: %w", err)
	}
	if !volumeFound {
		return infer.ReadResponse[VolumeArgs, VolumeState]{}, nil
	}

	instance, err := findVolumeInstance(ctx, client, inputs.ProjectID, volumeID, inputs.EnvironmentID, inputs.ServiceID)
	if err != nil {
		return infer.ReadResponse[VolumeArgs, VolumeState]{}, fmt.Errorf("read volume instances: %w", err)
	}
	if instance == nil {
		return infer.ReadResponse[VolumeArgs, VolumeState]{}, fmt.Errorf(
			"volume %s has no matching attachment", volumeID,
		)
	}
	inputs.ServiceID = instance.ServiceID
	inputs.MountPath = instance.MountPath
	inputs.EnvironmentID = instance.EnvironmentID

	inputs.Name = nil
	if volumeName != "" {
		inputs.Name = &volumeName
	}
	state := VolumeState{VolumeArgs: inputs, RailwayID: volumeID}
	return infer.ReadResponse[VolumeArgs, VolumeState]{ID: volumeID, Inputs: inputs, State: state}, nil
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type volumeInstance struct {
	VolumeID      string `json:"volumeId"`
	ServiceID     string `json:"serviceId"`
	MountPath     string `json:"mountPath"`
	EnvironmentID string `json:"environmentId"`
}

type volumeInstanceConnection struct {
	Edges []struct {
		Node volumeInstance `json:"node"`
	} `json:"edges"`
	PageInfo pageInfo `json:"pageInfo"`
}

func findProjectVolume(
	ctx context.Context, client *Client, projectID, volumeID string,
) (string, bool, error) {
	query := `query projectVolumes($projectId: String!, $after: String) {
  project(id: $projectId) {
    id
    volumes(first: 100, after: $after) {
      edges { node { id name } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	var after interface{}
	for {
		var result struct {
			Project struct {
				ID      string `json:"id"`
				Volumes struct {
					Edges []struct {
						Node struct {
							ID   string `json:"id"`
							Name string `json:"name"`
						} `json:"node"`
					} `json:"edges"`
					PageInfo pageInfo `json:"pageInfo"`
				} `json:"volumes"`
			} `json:"project"`
		}
		if err := client.query(ctx, query, map[string]interface{}{
			"projectId": projectID,
			"after":     after,
		}, &result); err != nil {
			return "", false, err
		}
		for _, edge := range result.Project.Volumes.Edges {
			if edge.Node.ID == volumeID {
				return edge.Node.Name, true, nil
			}
		}
		if !result.Project.Volumes.PageInfo.HasNextPage {
			return "", false, nil
		}
		if result.Project.Volumes.PageInfo.EndCursor == "" {
			return "", false, errors.New("railway returned a volume page without an end cursor")
		}
		after = result.Project.Volumes.PageInfo.EndCursor
	}
}

func findVolumeInstance(
	ctx context.Context,
	client *Client,
	projectID string,
	volumeID string,
	environmentID string,
	serviceID string,
) (*volumeInstance, error) {
	query := `query volumeInstances($projectId: String!, $after: String) {
  environments(projectId: $projectId, first: 50, after: $after) {
    edges {
      node {
        id
        volumeInstances(first: 100) {
          edges { node { volumeId serviceId mountPath environmentId } }
          pageInfo { hasNextPage endCursor }
        }
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}`
	var matches []volumeInstance
	var after interface{}
	for {
		var result struct {
			Environments struct {
				Edges []struct {
					Node struct {
						ID              string                   `json:"id"`
						VolumeInstances volumeInstanceConnection `json:"volumeInstances"`
					} `json:"node"`
				} `json:"edges"`
				PageInfo pageInfo `json:"pageInfo"`
			} `json:"environments"`
		}
		if err := client.query(ctx, query, map[string]interface{}{
			"projectId": projectID,
			"after":     after,
		}, &result); err != nil {
			return nil, err
		}
		for _, edge := range result.Environments.Edges {
			connection := edge.Node.VolumeInstances
			matches = appendMatchingVolumeInstances(
				matches, connection.Edges, volumeID, environmentID, serviceID,
			)
			if connection.PageInfo.HasNextPage {
				if connection.PageInfo.EndCursor == "" {
					return nil, errors.New("railway returned a volume instance page without an end cursor")
				}
				more, err := readEnvironmentVolumeInstances(
					ctx, client, edge.Node.ID, connection.PageInfo.EndCursor,
					volumeID, environmentID, serviceID,
				)
				if err != nil {
					return nil, err
				}
				matches = append(matches, more...)
			}
		}
		if !result.Environments.PageInfo.HasNextPage {
			break
		}
		if result.Environments.PageInfo.EndCursor == "" {
			return nil, errors.New("railway returned an environment page without an end cursor")
		}
		after = result.Environments.PageInfo.EndCursor
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf(
			"volume %s has multiple matching attachments; import with volumeId/projectId/environmentId/serviceId",
			volumeID,
		)
	}
}

func readEnvironmentVolumeInstances(
	ctx context.Context,
	client *Client,
	environmentID string,
	after string,
	volumeID string,
	wantedEnvironmentID string,
	serviceID string,
) ([]volumeInstance, error) {
	query := `query environmentVolumeInstances($environmentId: String!, $after: String) {
  environment(id: $environmentId) {
    id
    volumeInstances(first: 100, after: $after) {
      edges { node { volumeId serviceId mountPath environmentId } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	var matches []volumeInstance
	for {
		var result struct {
			Environment struct {
				ID              string                   `json:"id"`
				VolumeInstances volumeInstanceConnection `json:"volumeInstances"`
			} `json:"environment"`
		}
		if err := client.query(ctx, query, map[string]interface{}{
			"environmentId": environmentID,
			"after":         after,
		}, &result); err != nil {
			return nil, err
		}
		connection := result.Environment.VolumeInstances
		matches = appendMatchingVolumeInstances(
			matches, connection.Edges, volumeID, wantedEnvironmentID, serviceID,
		)
		if !connection.PageInfo.HasNextPage {
			return matches, nil
		}
		if connection.PageInfo.EndCursor == "" {
			return nil, errors.New("railway returned a volume instance page without an end cursor")
		}
		after = connection.PageInfo.EndCursor
	}
}

func appendMatchingVolumeInstances(
	matches []volumeInstance,
	edges []struct {
		Node volumeInstance `json:"node"`
	},
	volumeID string,
	environmentID string,
	serviceID string,
) []volumeInstance {
	for _, edge := range edges {
		node := edge.Node
		if node.VolumeID != volumeID ||
			(environmentID != "" && node.EnvironmentID != environmentID) ||
			(serviceID != "" && node.ServiceID != serviceID) {
			continue
		}
		matches = append(matches, node)
	}
	return matches
}

func (*Volume) Update(
	ctx context.Context, req infer.UpdateRequest[VolumeArgs, VolumeState],
) (infer.UpdateResponse[VolumeState], error) {
	input := req.Inputs
	state := req.State
	if req.DryRun {
		state.VolumeArgs = input
		return infer.UpdateResponse[VolumeState]{Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.UpdateResponse[VolumeState]{}, err
	}

	// Update volume name
	if !equalPointers(state.Name, input.Name) {
		if err := updateVolumeName(ctx, client, req.ID, input.Name); err != nil {
			return infer.UpdateResponse[VolumeState]{}, fmt.Errorf("update volume: %w", err)
		}
	}

	// Update mount path
	if input.MountPath != state.MountPath {
		mutation := `mutation volumeInstanceUpdate($volumeId: String!, $environmentId: String, $input: VolumeInstanceUpdateInput!) {
  volumeInstanceUpdate(volumeId: $volumeId, environmentId: $environmentId, input: $input)
}`
		vars := map[string]interface{}{
			"volumeId":      req.ID,
			"environmentId": input.EnvironmentID,
			"input":         map[string]interface{}{"mountPath": input.MountPath},
		}
		if err := client.mutate(ctx, mutation, vars, nil); err != nil {
			return infer.UpdateResponse[VolumeState]{}, fmt.Errorf("update volume mount: %w", err)
		}
	}

	state.VolumeArgs = input
	return infer.UpdateResponse[VolumeState]{Output: state}, nil
}

func (*Volume) Delete(
	ctx context.Context, req infer.DeleteRequest[VolumeState],
) (infer.DeleteResponse, error) {
	client, err := getClient(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}

	mutation := `mutation volumeDelete($volumeId: String!) { volumeDelete(volumeId: $volumeId) }`

	volumeID := req.ID
	if parts := strings.SplitN(req.ID, "/", 2); len(parts) == 2 {
		volumeID = parts[0]
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"volumeId": volumeID}, nil); err != nil {
		if isNotFound(err) {
			return infer.DeleteResponse{}, nil
		}
		return infer.DeleteResponse{}, fmt.Errorf("delete volume: %w", err)
	}

	return infer.DeleteResponse{}, nil
}

func updateVolumeName(ctx context.Context, client *Client, volumeID string, name *string) error {
	mutation := `mutation volumeUpdate($volumeId: String!, $input: VolumeUpdateInput!) {
  volumeUpdate(volumeId: $volumeId, input: $input) { id name }
}`
	vars := map[string]interface{}{
		"volumeId": volumeID,
		"input":    map[string]interface{}{"name": name},
	}
	return client.mutate(ctx, mutation, vars, nil)
}
