package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Bucket is an S3-compatible Railway object storage bucket. Buckets are
// created at the project level and deployed to an environment with a region.
// Each environment has its own bucket instance with isolated credentials.
type Bucket struct{}

func (bucket *Bucket) Annotate(a infer.Annotator) {
	a.Describe(bucket, "An S3-compatible Railway object storage bucket deployed to one environment.")
	a.SetToken("index", "Bucket")
	a.AddAlias("pkg", "Bucket")
}

type BucketArgs struct {
	// Project ID that owns the bucket.
	ProjectID string `pulumi:"projectId" provider:"replaceOnChanges"`
	// Environment ID the bucket is deployed to.
	EnvironmentID string `pulumi:"environmentId" provider:"replaceOnChanges"`
	// Bucket region: sjc, iad, ams, or sin. Immutable after creation.
	Region string `pulumi:"region" provider:"replaceOnChanges"`
	// Optional display name. Railway generates one when omitted.
	Name *string `pulumi:"name,optional"`
}

func (args *BucketArgs) Annotate(a infer.Annotator) {
	a.Describe(args, "Inputs for creating a Railway object storage bucket.")
	a.Describe(&args.ProjectID, "ID of the Railway project that owns the bucket.")
	a.Describe(&args.EnvironmentID, "ID of the environment the bucket is deployed to.")
	a.Describe(&args.Region, "Bucket region: sjc (US West), iad (US East), ams (EU West), or sin (Asia Pacific). It cannot change after creation.")
	a.Describe(&args.Name, "Display name for the bucket. Railway generates a name when omitted.")
}

type BucketState struct {
	BucketArgs
	// Railway bucket ID (immutable).
	RailwayID string `pulumi:"railwayId"`
	// S3 endpoint, for example https://storage.railway.app.
	Endpoint string `pulumi:"endpoint"`
	// S3 access key ID for this environment's bucket instance.
	AccessKeyID string `pulumi:"accessKeyId"`
	// S3 secret access key for this environment's bucket instance.
	SecretAccessKey string `pulumi:"secretAccessKey" provider:"secret"`
	// Globally unique S3 bucket name (display name plus a short hash).
	S3BucketName string `pulumi:"s3BucketName"`
	// S3 region reported by Railway, for example "auto".
	S3Region string `pulumi:"s3Region"`
	// URL style for S3 clients: virtual or path.
	URLStyle string `pulumi:"urlStyle"`
}

func (state *BucketState) Annotate(a infer.Annotator) {
	a.Describe(state, "The observed state of a Railway object storage bucket.")
	a.Describe(&state.ProjectID, "ID of the Railway project that owns the bucket.")
	a.Describe(&state.EnvironmentID, "ID of the environment the bucket is deployed to.")
	a.Describe(&state.Region, "Bucket region.")
	a.Describe(&state.Name, "Bucket display name.")
	a.Describe(&state.RailwayID, "Immutable Railway bucket ID.")
	a.Describe(&state.Endpoint, "S3 API endpoint, for example https://storage.railway.app.")
	a.Describe(&state.AccessKeyID, "S3 access key ID for the bucket's environment instance.")
	a.Describe(&state.SecretAccessKey, "S3 secret access key for the bucket's environment instance.")
	a.Describe(&state.S3BucketName, "Globally unique bucket name for the S3 API (display name plus hash).")
	a.Describe(&state.S3Region, "Region value S3 clients should use, for example auto.")
	a.Describe(&state.URLStyle, "URL style S3 clients should use: virtual or path.")
}

var bucketRegions = map[string]string{
	"sjc": "US West (California)",
	"iad": "US East (Virginia)",
	"ams": "EU West (Amsterdam)",
	"sin": "Asia Pacific (Singapore)",
}

func (*Bucket) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[BucketArgs], error) {
	return checkInputs(ctx, req, func(inputs property.Map, input BucketArgs) []provider.CheckFailure {
		failures := appendFailures(
			required(inputs, "projectId", input.ProjectID),
			required(inputs, "environmentId", input.EnvironmentID),
			required(inputs, "region", input.Region),
		)
		if !isUnknown(inputs, "region") && input.Region != "" {
			if _, ok := bucketRegions[input.Region]; !ok {
				failures = append(failures, provider.CheckFailure{
					Property: "region",
					Reason:   "region must be one of: sjc, iad, ams, sin",
				})
			}
		}
		if input.Name != nil && !isUnknown(inputs, "name") && strings.TrimSpace(*input.Name) == "" {
			failures = append(failures, provider.CheckFailure{
				Property: "name", Reason: "name must not be empty when provided",
			})
		}
		return failures
	})
}

func (*Bucket) Create(
	ctx context.Context, req infer.CreateRequest[BucketArgs],
) (infer.CreateResponse[BucketState], error) {
	input := req.Inputs
	state := BucketState{BucketArgs: input}
	if req.DryRun {
		return infer.CreateResponse[BucketState]{ID: req.Name, Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.CreateResponse[BucketState]{}, err
	}

	// 1. Create the bucket at the project level.
	var createResult struct {
		BucketCreate struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"bucketCreate"`
	}

	mutation := `mutation bucketCreate($input: BucketCreateInput!) {
  bucketCreate(input: $input) { id name }
}`

	createInput := map[string]interface{}{"projectId": input.ProjectID}
	if input.Name != nil {
		createInput["name"] = *input.Name
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": createInput}, &createResult); err != nil {
		return infer.CreateResponse[BucketState]{}, fmt.Errorf("create bucket: %w", err)
	}
	if err := requireCreatedID("bucket", createResult.BucketCreate.ID); err != nil {
		return infer.CreateResponse[BucketState]{}, err
	}

	bucketID := createResult.BucketCreate.ID
	state.RailwayID = bucketID
	state.Name = &createResult.BucketCreate.Name

	// 2. Deploy the bucket to the environment with its region.
	if err := deployBucketToEnvironment(ctx, client, bucketID, input.EnvironmentID, input.Region, fmt.Sprintf("Create bucket %s", createResult.BucketCreate.Name)); err != nil {
		return infer.CreateResponse[BucketState]{ID: bucketID, Output: state}, infer.ResourceInitFailedError{
			Reasons: []string{fmt.Sprintf("deploy bucket to environment: %v", err)},
		}
	}

	// 3. Fetch S3 credentials for the deployed instance.
	if err := readBucketCredentials(ctx, client, input.ProjectID, input.EnvironmentID, bucketID, &state); err != nil {
		return infer.CreateResponse[BucketState]{ID: bucketID, Output: state}, infer.ResourceInitFailedError{
			Reasons: []string{fmt.Sprintf("read bucket credentials: %v", err)},
		}
	}

	return infer.CreateResponse[BucketState]{ID: bucketID, Output: state}, nil
}

func (*Bucket) Read(
	ctx context.Context, req infer.ReadRequest[BucketArgs, BucketState],
) (infer.ReadResponse[BucketArgs, BucketState], error) {
	state := req.State
	inputs := req.Inputs
	if strings.TrimSpace(inputs.ProjectID) == "" {
		parts := strings.SplitN(req.ID, "/", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return infer.ReadResponse[BucketArgs, BucketState]{}, fmt.Errorf(
				"bucket import ID must be projectId/environmentId/bucketId",
			)
		}
		inputs.ProjectID = parts[0]
		inputs.EnvironmentID = parts[1]
		state.RailwayID = parts[2]
	}
	if strings.TrimSpace(inputs.EnvironmentID) == "" {
		return infer.ReadResponse[BucketArgs, BucketState]{}, fmt.Errorf(
			"bucket import ID must be projectId/environmentId/bucketId",
		)
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.ReadResponse[BucketArgs, BucketState]{}, err
	}

	// Project-level bucket existence (paginated like volumes) plus the
	// deployed environment config.
	bucket, bucketFound, err := findProjectBucket(ctx, client, inputs.ProjectID, state.RailwayID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[BucketArgs, BucketState]{}, nil
		}
		return infer.ReadResponse[BucketArgs, BucketState]{}, fmt.Errorf("read project buckets: %w", err)
	}
	if !bucketFound {
		return infer.ReadResponse[BucketArgs, BucketState]{}, nil
	}

	state.RailwayID = bucket.ID
	if bucket.Name != nil {
		state.Name = bucket.Name
	}

	// Region comes from the environment config's bucket instance.
	region, err := readBucketRegion(ctx, client, inputs.EnvironmentID, state.RailwayID)
	if err != nil {
		if isNotFound(err) {
			// The environment itself is gone, so the deployment is gone.
			return infer.ReadResponse[BucketArgs, BucketState]{}, nil
		}
		return infer.ReadResponse[BucketArgs, BucketState]{}, fmt.Errorf("read bucket region: %w", err)
	}
	state.EnvironmentID = inputs.EnvironmentID
	state.ProjectID = inputs.ProjectID
	if region == nil {
		// Project-level bucket exists but is not deployed to this
		// environment (partial create, or the deploy patch never landed).
		// Keep it in state so Update can re-commit the deployment;
		// reporting gone would drop it from state and orphan the bucket.
		// Credentials are unavailable until the instance is deployed.
		if state.Region == "" {
			state.Region = inputs.Region
		}
		return infer.ReadResponse[BucketArgs, BucketState]{ID: state.RailwayID, Inputs: state.BucketArgs, State: state}, nil
	}
	state.Region = *region

	// Credentials describe the deployed instance; keep them fresh on refresh.
	// A credentials failure is surfaced rather than swallowed: stale keys
	// break S3 clients and must be visible in the Pulumi error, not silent.
	if err := readBucketCredentials(ctx, client, inputs.ProjectID, inputs.EnvironmentID, state.RailwayID, &state); err != nil {
		return infer.ReadResponse[BucketArgs, BucketState]{}, fmt.Errorf("read bucket credentials: %w", err)
	}

	return infer.ReadResponse[BucketArgs, BucketState]{ID: state.RailwayID, Inputs: state.BucketArgs, State: state}, nil
}

func (*Bucket) Update(
	ctx context.Context, req infer.UpdateRequest[BucketArgs, BucketState],
) (infer.UpdateResponse[BucketState], error) {
	input := req.Inputs
	state := req.State
	if req.DryRun {
		state.BucketArgs = input
		return infer.UpdateResponse[BucketState]{Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.UpdateResponse[BucketState]{}, err
	}

	// Rename when the display name changed.
	if input.Name != nil && (state.Name == nil || *input.Name != *state.Name) {
		mutation := `mutation bucketUpdate($id: String!, $input: BucketUpdateInput!) {
  bucketUpdate(id: $id, input: $input) { id name }
}`
		var updateResult struct {
			BucketUpdate struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"bucketUpdate"`
		}
		if err := client.mutate(ctx, mutation, map[string]interface{}{
			"id":    req.ID,
			"input": map[string]interface{}{"name": *input.Name},
		}, &updateResult); err != nil {
			return infer.UpdateResponse[BucketState]{}, fmt.Errorf("rename bucket: %w", err)
		}
		state.Name = &updateResult.BucketUpdate.Name
	} else if input.Name == nil && state.Name != nil {
		// Clearing name is not supported by the API (rename needs a value);
		// keep the effective name in state so previews stay honest.
		input.Name = state.Name
	}

	// Re-commit the deployment patch. It is idempotent and also repairs
	// partial creates where the bucket exists but never deployed.
	commitMessage := "Update bucket"
	if input.Name != nil {
		commitMessage = fmt.Sprintf("Update bucket %s", *input.Name)
	}
	if err := deployBucketToEnvironment(ctx, client, req.ID, input.EnvironmentID, input.Region, commitMessage); err != nil {
		return infer.UpdateResponse[BucketState]{}, fmt.Errorf("deploy bucket to environment: %w", err)
	}

	state.BucketArgs = input
	if err := readBucketCredentials(ctx, client, input.ProjectID, input.EnvironmentID, req.ID, &state); err != nil {
		return infer.UpdateResponse[BucketState]{}, fmt.Errorf("read bucket credentials: %w", err)
	}
	return infer.UpdateResponse[BucketState]{Output: state}, nil
}

func (*Bucket) Delete(
	ctx context.Context, req infer.DeleteRequest[BucketState],
) (infer.DeleteResponse, error) {
	client, err := getClient(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}

	// Deleting the bucket from its environment also removes it from the
	// project: the patch with isDeleted is the full deletion path.
	patch := map[string]interface{}{
		"buckets": map[string]interface{}{
			req.ID: map[string]interface{}{"isDeleted": true},
		},
	}

	mutation := `mutation environmentPatchCommit($environmentId: String!, $patch: EnvironmentConfig!, $commitMessage: String) {
  environmentPatchCommit(environmentId: $environmentId, patch: $patch, commitMessage: $commitMessage)
}`

	if err := client.mutate(ctx, mutation, map[string]interface{}{
		"environmentId": req.State.EnvironmentID,
		"patch":         patch,
		"commitMessage": "Delete bucket",
	}, nil); err != nil {
		if isNotFound(err) {
			return infer.DeleteResponse{}, nil
		}
		return infer.DeleteResponse{}, fmt.Errorf("delete bucket: %w", err)
	}

	// environmentPatchCommit can land in a staged changeset when the
	// environment has unmerged changes, in which case the bucket still
	// exists and bills. Verify the deletion took effect by reading the
	// bucket back, with a bounded retry for commit propagation.
	if err := verifyBucketDeleted(ctx, client, req.State.ProjectID, req.ID); err != nil {
		return infer.DeleteResponse{}, fmt.Errorf("delete bucket: %w", err)
	}

	return infer.DeleteResponse{}, nil
}

// verifyBucketDeleted reads the project bucket back and fails the delete
// unless the bucket is gone. It retries briefly because
// environmentPatchCommit's ack can race the actual deletion.
func verifyBucketDeleted(ctx context.Context, client *Client, projectID, bucketID string) error {
	const attempts = 5
	const interval = time.Second
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, interval); err != nil {
				return fmt.Errorf("wait to verify bucket deletion: %w", err)
			}
		}
		_, found, err := findProjectBucket(ctx, client, projectID, bucketID)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			lastErr = err
			continue
		}
		if !found {
			return nil
		}
		lastErr = fmt.Errorf(
			"bucket %s still exists after the delete patch; the environment may have staged changes (railway environment edit commits them)",
			bucketID,
		)
	}
	if lastErr == nil {
		return errors.New("verify bucket deletion: exhausted retries")
	}
	return lastErr
}

// deployBucketToEnvironment commits an environment patch that deploys the
// bucket with its region. Railway treats repeated identical patches as
// no-ops, so calling it on every update is safe.
func deployBucketToEnvironment(
	ctx context.Context, client *Client, bucketID, environmentID, region, commitMessage string,
) error {
	patch := map[string]interface{}{
		"buckets": map[string]interface{}{
			bucketID: map[string]interface{}{
				"region":    region,
				"isCreated": true,
			},
		},
	}

	mutation := `mutation environmentPatchCommit($environmentId: String!, $patch: EnvironmentConfig!, $commitMessage: String) {
  environmentPatchCommit(environmentId: $environmentId, patch: $patch, commitMessage: $commitMessage)
}`

	return client.mutate(ctx, mutation, map[string]interface{}{
		"environmentId": environmentID,
		"patch":         patch,
		"commitMessage": commitMessage,
	}, nil)
}

// readBucketCredentials loads the S3 credentials for the bucket's instance in
// the environment into state.
func readBucketCredentials(
	ctx context.Context, client *Client, projectID, environmentID, bucketID string, state *BucketState,
) error {
	var result struct {
		BucketS3Credentials []struct {
			AccessKeyID     string `json:"accessKeyId"`
			SecretAccessKey string `json:"secretAccessKey"`
			Endpoint        string `json:"endpoint"`
			BucketName      string `json:"bucketName"`
			Region          string `json:"region"`
			URLStyle        string `json:"urlStyle"`
		} `json:"bucketS3Credentials"`
	}

	query := `query bucketS3Credentials($projectId: String!, $environmentId: String!, $bucketId: String!) {
  bucketS3Credentials(projectId: $projectId, environmentId: $environmentId, bucketId: $bucketId) {
    accessKeyId secretAccessKey endpoint bucketName region urlStyle
  }
}`

	if err := client.query(ctx, query, map[string]interface{}{
		"projectId":     projectID,
		"environmentId": environmentID,
		"bucketId":      bucketID,
	}, &result); err != nil {
		return err
	}
	if len(result.BucketS3Credentials) == 0 {
		return fmt.Errorf("railway returned no S3 credentials for bucket %s", bucketID)
	}
	credentials := result.BucketS3Credentials[0]
	state.AccessKeyID = credentials.AccessKeyID
	state.SecretAccessKey = credentials.SecretAccessKey
	state.Endpoint = credentials.Endpoint
	state.S3BucketName = credentials.BucketName
	state.S3Region = credentials.Region
	state.URLStyle = credentials.URLStyle
	return nil
}

// readBucketRegion returns the deployed region for the bucket instance in the
// environment, or nil when the bucket is not deployed there.
func readBucketRegion(
	ctx context.Context, client *Client, environmentID, bucketID string,
) (*string, error) {
	var result struct {
		Environment struct {
			Config json.RawMessage `json:"config"`
		} `json:"environment"`
	}

	query := `query environmentConfig($id: String!) {
  environment(id: $id) { config }
}`

	if err := client.query(ctx, query, map[string]interface{}{"id": environmentID}, &result); err != nil {
		return nil, err
	}

	var config struct {
		Buckets map[string]struct {
			Region    *string `json:"region"`
			IsDeleted *bool   `json:"isDeleted"`
		} `json:"buckets"`
	}
	if len(result.Environment.Config) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(result.Environment.Config, &config); err != nil {
		return nil, fmt.Errorf("decode environment config: %w", err)
	}

	instance, ok := config.Buckets[bucketID]
	if !ok || (instance.IsDeleted != nil && *instance.IsDeleted) {
		return nil, nil
	}
	return instance.Region, nil
}

// projectBucket is the project-level view of a bucket from the paginated
// project query.
type projectBucket struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

// findProjectBucket locates a bucket by ID across paginated project buckets,
// mirroring findProjectVolume in volume.go.
func findProjectBucket(
	ctx context.Context, client *Client, projectID, bucketID string,
) (*projectBucket, bool, error) {
	query := `query projectBuckets($id: String!, $after: String) {
  project(id: $id) {
    buckets(first: 100, after: $after) {
      edges { node { id name } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`
	var after interface{}
	for {
		var result struct {
			Project struct {
				Buckets struct {
					Edges []struct {
						Node projectBucket `json:"node"`
					} `json:"edges"`
					PageInfo pageInfo `json:"pageInfo"`
				} `json:"buckets"`
			} `json:"project"`
		}
		if err := client.query(ctx, query, map[string]interface{}{
			"id":    projectID,
			"after": after,
		}, &result); err != nil {
			return nil, false, err
		}
		for _, edge := range result.Project.Buckets.Edges {
			if edge.Node.ID == bucketID {
				return &edge.Node, true, nil
			}
		}
		if !result.Project.Buckets.PageInfo.HasNextPage {
			return nil, false, nil
		}
		if result.Project.Buckets.PageInfo.EndCursor == "" {
			return nil, false, errors.New("railway returned a bucket page without an end cursor")
		}
		after = result.Project.Buckets.PageInfo.EndCursor
	}
}
