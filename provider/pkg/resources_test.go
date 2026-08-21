package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

type capturedGraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
}

func TestServiceCreateReturnsPartialStateWhenInstanceConfigurationFails(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(request.Query, "serviceCreate") {
				t.Errorf("expected serviceCreate, got %s", request.Query)
			}
			writeGraphQL(t, w, map[string]interface{}{
				"serviceCreate": map[string]interface{}{"id": "service-1", "name": "web"},
			})
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"message":"invalid start command"}]}`))
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	start := "npm start"
	response, err := (&Service{}).Create(ctx, infer.CreateRequest[ServiceArgs]{
		Name: "web",
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", StartCommand: &start,
		},
	})
	var initErr infer.ResourceInitFailedError
	if !errors.As(err, &initErr) {
		t.Fatalf("expected ResourceInitFailedError, got %v", err)
	}
	if response.ID != "service-1" || response.Output.RailwayID != "service-1" {
		t.Fatalf("partial response did not preserve service ID: %#v", response)
	}
}

func TestServiceUpdateSendsExplicitNullForClearedSettings(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		input := request.Variables["input"].(map[string]interface{})
		if value, exists := input["startCommand"]; !exists || value != nil {
			t.Errorf("startCommand = %#v, exists=%v; want explicit null", value, exists)
		}
		writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
	}))
	defer server.Close()

	start := "npm start"
	old := ServiceArgs{
		ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", StartCommand: &start,
	}
	next := ServiceArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web"}
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		State: ServiceState{
			ServiceArgs: old, RailwayID: "service-1", InstanceID: "instance-1",
		},
		Inputs: next,
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if response.Output.StartCommand != nil {
		t.Fatalf("start command remained in state: %#v", response.Output.StartCommand)
	}
}

func TestVariableCreateSendsNameField(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "query variables") {
			writeGraphQL(t, w, map[string]interface{}{"variables": map[string]string{}})
			return
		}
		input := request.Variables["input"].(map[string]interface{})
		if input["name"] != "NODE_ENV" {
			t.Errorf("variableUpsert input name = %#v, want NODE_ENV", input["name"])
		}
		if _, exists := input["key"]; exists {
			t.Errorf("variableUpsert input must not contain key: %#v", input)
		}
		writeGraphQL(t, w, map[string]interface{}{"variableUpsert": true})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Variable{}).Create(ctx, infer.CreateRequest[VariableArgs]{
		Inputs: VariableArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Key: "NODE_ENV", Value: "production",
		},
	})
	if err != nil {
		t.Fatalf("variable create failed: %v", err)
	}
}

func TestVariableCreateFailsWhenKeyAlreadyExists(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(request.Query, "query variables") {
			t.Errorf("create should not mutate an existing variable: %s", request.Query)
		}
		writeGraphQL(t, w, map[string]interface{}{
			"variables": map[string]string{"NODE_ENV": "staging"},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Variable{}).Create(ctx, infer.CreateRequest[VariableArgs]{
		Inputs: VariableArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Key: "NODE_ENV", Value: "production",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") ||
		!strings.Contains(err.Error(), "project-1/environment-1//NODE_ENV") {
		t.Fatalf("expected import guidance, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected only the existence query, got %d calls", calls.Load())
	}
}

func TestVariableReadDetectsDriftAndDeletion(t *testing.T) {
	t.Parallel()
	var exists atomic.Bool
	exists.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		variables := map[string]string{}
		if exists.Load() {
			variables["NODE_ENV"] = "staging"
		}
		writeGraphQL(t, w, map[string]interface{}{"variables": variables})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	inputs := VariableArgs{
		ProjectID: "project-1", EnvironmentID: "environment-1", Key: "NODE_ENV", Value: "production",
	}
	response, err := (&Variable{}).Read(ctx, infer.ReadRequest[VariableArgs, VariableState]{
		ID: "project-1/environment-1//NODE_ENV", Inputs: inputs,
	})
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if response.Inputs.Value != "staging" || response.State.Value != "staging" {
		t.Fatalf("drift was not reflected: %#v", response)
	}

	exists.Store(false)
	response, err = (&Variable{}).Read(ctx, infer.ReadRequest[VariableArgs, VariableState]{
		ID: "project-1/environment-1//NODE_ENV", Inputs: inputs,
	})
	if err != nil {
		t.Fatalf("read deletion failed: %v", err)
	}
	if response.ID != "" {
		t.Fatalf("deleted variable remained in state: %#v", response)
	}
}

func TestVariableImportParsesCompositeID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := request.Variables["serviceId"]; got != "service-1" {
			t.Errorf("serviceId = %#v, want service-1", got)
		}
		writeGraphQL(t, w, map[string]interface{}{"variables": map[string]string{"API_KEY": "secret"}})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Variable{}).Read(ctx, infer.ReadRequest[VariableArgs, VariableState]{
		ID: "project-1/environment-1/service-1/API_KEY",
	})
	if err != nil {
		t.Fatalf("import read failed: %v", err)
	}
	if response.Inputs.ProjectID != "project-1" || response.Inputs.ServiceID == nil ||
		*response.Inputs.ServiceID != "service-1" || response.Inputs.Key != "API_KEY" {
		t.Fatalf("invalid imported inputs: %#v", response.Inputs)
	}
}

func TestVolumeReadReconstructsImportedState(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "projectVolumes"):
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"id": "project-1",
					"volumes": map[string]interface{}{"edges": []interface{}{
						map[string]interface{}{"node": map[string]interface{}{"id": "volume-1", "name": "data"}},
					}},
				},
			})
		case strings.Contains(request.Query, "volumeInstances"):
			writeGraphQL(t, w, map[string]interface{}{
				"environments": map[string]interface{}{"edges": []interface{}{
					map[string]interface{}{"node": map[string]interface{}{
						"id": "environment-1",
						"volumeInstances": map[string]interface{}{"edges": []interface{}{
							map[string]interface{}{"node": map[string]interface{}{
								"volumeId": "volume-1", "serviceId": "service-1",
								"mountPath": "/data", "environmentId": "environment-1",
							}},
						}},
					}},
				}},
			})
		default:
			t.Errorf("unexpected query: %s", request.Query)
		}
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Volume{}).Read(ctx, infer.ReadRequest[VolumeArgs, VolumeState]{ID: "volume-1/project-1"})
	if err != nil {
		t.Fatalf("volume import failed: %v", err)
	}
	if response.Inputs.ProjectID != "project-1" || response.Inputs.EnvironmentID != "environment-1" ||
		response.Inputs.ServiceID != "service-1" || response.Inputs.MountPath != "/data" ||
		response.Inputs.Name == nil || *response.Inputs.Name != "data" {
		t.Fatalf("invalid imported volume state: %#v", response.Inputs)
	}
}

func TestVolumeCreateSetsNameViaFollowUpUpdate(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			input := request.Variables["input"].(map[string]interface{})
			if _, exists := input["name"]; exists {
				t.Errorf("volumeCreate input must not contain name: %#v", input)
			}
			if input["environmentId"] != "environment-1" {
				t.Errorf("volumeCreate input missing environmentId: %#v", input)
			}
			writeGraphQL(t, w, map[string]interface{}{
				"volumeCreate": map[string]interface{}{"id": "volume-1", "name": ""},
			})
		case 2:
			if request.Variables["volumeId"] != "volume-1" {
				t.Errorf("volumeUpdate volumeId = %#v, want volume-1", request.Variables["volumeId"])
			}
			input := request.Variables["input"].(map[string]interface{})
			if input["name"] != "data" {
				t.Errorf("volumeUpdate input name = %#v, want data", input["name"])
			}
			writeGraphQL(t, w, map[string]interface{}{
				"volumeUpdate": map[string]interface{}{"id": "volume-1", "name": "data"},
			})
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	name := "data"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Volume{}).Create(ctx, infer.CreateRequest[VolumeArgs]{
		Inputs: VolumeArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1",
			ServiceID: "service-1", MountPath: "/data", Name: &name,
		},
	})
	if err != nil {
		t.Fatalf("volume create failed: %v", err)
	}
	if response.ID != "volume-1" {
		t.Fatalf("unexpected volume ID: %#v", response)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected volumeCreate + volumeUpdate, got %d calls", calls.Load())
	}
}

func TestCreateRejectsEmptyRailwayIDs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		create func(context.Context) error
		field  string
	}{
		{
			name: "project",
			create: func(ctx context.Context) error {
				_, err := (&Project{}).Create(ctx, infer.CreateRequest[ProjectArgs]{
					Inputs: ProjectArgs{Name: "project"},
				})
				return err
			},
			field: "projectCreate",
		},
		{
			name: "service",
			create: func(ctx context.Context) error {
				_, err := (&Service{}).Create(ctx, infer.CreateRequest[ServiceArgs]{
					Inputs: ServiceArgs{
						ProjectID: "project-1", EnvironmentID: "environment-1", Name: "service",
					},
				})
				return err
			},
			field: "serviceCreate",
		},
		{
			name: "custom domain",
			create: func(ctx context.Context) error {
				_, err := (&CustomDomainResource{}).Create(ctx, infer.CreateRequest[CustomDomainArgs]{
					Inputs: CustomDomainArgs{
						ProjectID: "project-1", EnvironmentID: "environment-1",
						ServiceID: "service-1", Domain: "api.example.com",
					},
				})
				return err
			},
			field: "customDomainCreate",
		},
		{
			name: "volume",
			create: func(ctx context.Context) error {
				_, err := (&Volume{}).Create(ctx, infer.CreateRequest[VolumeArgs]{
					Inputs: VolumeArgs{
						ProjectID: "project-1", EnvironmentID: "environment-1",
						ServiceID: "service-1", MountPath: "/data",
					},
				})
				return err
			},
			field: "volumeCreate",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeGraphQL(t, w, map[string]interface{}{test.field: nil})
			}))
			defer server.Close()
			ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
			err := test.create(ctx)
			if err == nil || !strings.Contains(err.Error(), "empty ID") {
				t.Fatalf("expected empty ID error, got %v", err)
			}
		})
	}
}

func TestProjectCreateOmitsUnsetDescription(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		input := request.Variables["input"].(map[string]interface{})
		if _, exists := input["description"]; exists {
			t.Fatalf("unset description must be omitted: %#v", input)
		}
		writeGraphQL(t, w, map[string]interface{}{
			"projectCreate": map[string]interface{}{
				"id": "project-1", "name": "project", "primaryEnvironmentId": "environment-1",
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Project{}).Create(ctx, infer.CreateRequest[ProjectArgs]{
		Inputs: ProjectArgs{Name: "project"},
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if response.ID != "project-1" {
		t.Fatalf("project ID = %q", response.ID)
	}
}

func TestProjectReadHydratesWorkspaceID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeGraphQL(t, w, map[string]interface{}{
			"project": map[string]interface{}{
				"id": "project-1", "name": "project", "description": "",
				"primaryEnvironmentId": "environment-1", "workspaceId": "workspace-1",
			},
		})
	}))
	defer server.Close()
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Project{}).Read(ctx, infer.ReadRequest[ProjectArgs, ProjectState]{ID: "project-1"})
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	if response.Inputs.WorkspaceID == nil || *response.Inputs.WorkspaceID != "workspace-1" {
		t.Fatalf("workspace ID was not hydrated: %#v", response.Inputs)
	}
}

func TestServiceReadHydratesBranch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "query service(") {
			writeGraphQL(t, w, map[string]interface{}{
				"service": map[string]interface{}{
					"id": "service-1", "name": "web", "projectId": "project-1", "branch": "main",
				},
			})
			return
		}
		if strings.Contains(request.Query, "environmentConfig") {
			writeGraphQL(t, w, map[string]interface{}{
				"environment": map[string]interface{}{"config": json.RawMessage(`{}`)},
			})
			return
		}
		writeGraphQL(t, w, map[string]interface{}{
			"serviceInstance": map[string]interface{}{"id": "instance-1"},
		})
	}))
	defer server.Close()
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Service{}).Read(ctx, infer.ReadRequest[ServiceArgs, ServiceState]{
		ID: "service-1/environment-1",
	})
	if err != nil {
		t.Fatalf("read service: %v", err)
	}
	if response.Inputs.Branch == nil || *response.Inputs.Branch != "main" {
		t.Fatalf("branch was not hydrated: %#v", response.Inputs)
	}
}

func TestServiceDeleteDeletesWholeService(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "environmentId") {
			t.Errorf("serviceDelete must not include environmentId: %s", request.Query)
		}
		if _, exists := request.Variables["environmentId"]; exists {
			t.Errorf("serviceDelete variables include environmentId: %#v", request.Variables)
		}
		writeGraphQL(t, w, map[string]interface{}{"serviceDelete": true})
	}))
	defer server.Close()
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Service{}).Delete(ctx, infer.DeleteRequest[ServiceState]{
		ID: "service-1", State: ServiceState{ServiceArgs: ServiceArgs{EnvironmentID: "environment-1"}},
	})
	if err != nil {
		t.Fatalf("delete service: %v", err)
	}
}

func TestCustomDomainReadHydratesTargetPortAndCNAME(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeGraphQL(t, w, map[string]interface{}{
			"customDomain": map[string]interface{}{
				"id": "domain-1", "domain": "api.example.com", "targetPort": 8080,
				"status": map[string]interface{}{
					"verificationToken": "verify", "certificateStatus": "ISSUED",
					"dnsRecords": []interface{}{
						map[string]interface{}{"hostlabel": "@", "requiredValue": "target.railway.app"},
					},
				},
			},
		})
	}))
	defer server.Close()
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&CustomDomainResource{}).Read(
		ctx,
		infer.ReadRequest[CustomDomainArgs, CustomDomainState]{
			ID: "domain-1/project-1/environment-1/service-1",
		},
	)
	if err != nil {
		t.Fatalf("read custom domain: %v", err)
	}
	if response.Inputs.TargetPort == nil || *response.Inputs.TargetPort != 8080 ||
		response.State.CNAMETarget != "target.railway.app" {
		t.Fatalf("custom domain fields were not hydrated: %#v", response)
	}
}

func TestVolumeReadPaginatesVolumesEnvironmentsAndInstances(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(request.Query, "projectVolumes") && request.Variables["after"] == nil:
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"id": "project-1",
					"volumes": map[string]interface{}{
						"edges":    []interface{}{},
						"pageInfo": map[string]interface{}{"hasNextPage": true, "endCursor": "volumes-1"},
					},
				},
			})
		case strings.Contains(request.Query, "projectVolumes"):
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"id": "project-1",
					"volumes": map[string]interface{}{
						"edges": []interface{}{
							map[string]interface{}{"node": map[string]interface{}{"id": "volume-1", "name": "data"}},
						},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
		case strings.Contains(request.Query, "query volumeInstances") && request.Variables["after"] == nil:
			writeGraphQL(t, w, map[string]interface{}{
				"environments": map[string]interface{}{
					"edges":    []interface{}{},
					"pageInfo": map[string]interface{}{"hasNextPage": true, "endCursor": "environments-1"},
				},
			})
		case strings.Contains(request.Query, "query volumeInstances"):
			writeGraphQL(t, w, map[string]interface{}{
				"environments": map[string]interface{}{
					"edges": []interface{}{
						map[string]interface{}{"node": map[string]interface{}{
							"id": "environment-1",
							"volumeInstances": map[string]interface{}{
								"edges": []interface{}{},
								"pageInfo": map[string]interface{}{
									"hasNextPage": true, "endCursor": "instances-1",
								},
							},
						}},
					},
					"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
				},
			})
		case strings.Contains(request.Query, "environmentVolumeInstances"):
			writeGraphQL(t, w, map[string]interface{}{
				"environment": map[string]interface{}{
					"id": "environment-1",
					"volumeInstances": map[string]interface{}{
						"edges": []interface{}{
							map[string]interface{}{"node": map[string]interface{}{
								"volumeId": "volume-1", "serviceId": "service-1",
								"mountPath": "/data", "environmentId": "environment-1",
							}},
						},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
		default:
			t.Fatalf("unexpected query: %s, variables: %#v", request.Query, request.Variables)
		}
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Volume{}).Read(ctx, infer.ReadRequest[VolumeArgs, VolumeState]{
		ID: "volume-1/project-1",
	})
	if err != nil {
		t.Fatalf("read volume: %v", err)
	}
	if response.Inputs.ServiceID != "service-1" || response.Inputs.EnvironmentID != "environment-1" {
		t.Fatalf("volume attachment was not found after pagination: %#v", response.Inputs)
	}
}

func TestCustomDomainCreateIncludesTargetPort(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		input := request.Variables["input"].(map[string]interface{})
		if input["targetPort"] != float64(8080) {
			t.Errorf("targetPort = %#v, want 8080", input["targetPort"])
		}
		writeGraphQL(t, w, map[string]interface{}{
			"customDomainCreate": map[string]interface{}{
				"id": "domain-1", "domain": "api.example.com",
				"status": map[string]interface{}{"verificationToken": "verify", "dnsRecords": []interface{}{}},
			},
		})
	}))
	defer server.Close()

	port := 8080
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&CustomDomainResource{}).Create(ctx, infer.CreateRequest[CustomDomainArgs]{
		Inputs: CustomDomainArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", ServiceID: "service-1",
			Domain: "api.example.com", TargetPort: &port,
		},
	})
	if err != nil {
		t.Fatalf("create domain failed: %v", err)
	}
}

func TestProjectUpdateClearsDescriptionAndDeleteIsIdempotent(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			input := request.Variables["input"].(map[string]interface{})
			if description, exists := input["description"]; !exists || description != nil {
				t.Errorf("description = %#v, exists=%v; want explicit null", description, exists)
			}
			writeGraphQL(t, w, map[string]interface{}{"projectUpdate": map[string]interface{}{"id": "project-1"}})
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"message":"Project not found","extensions":{"code":"NOT_FOUND"}}]}`))
		default:
			t.Fatalf("unexpected request")
		}
	}))
	defer server.Close()
	description := "old"
	state := ProjectState{
		ProjectArgs: ProjectArgs{Name: "project", Description: &description},
		RailwayID:   "project-1",
	}
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Project{}).Update(ctx, infer.UpdateRequest[ProjectArgs, ProjectState]{
		ID: "project-1", State: state, Inputs: ProjectArgs{Name: "project"},
	})
	if err != nil {
		t.Fatalf("update project: %v", err)
	}
	if response.Output.Description != nil {
		t.Fatalf("description remained in state: %#v", response.Output)
	}
	if _, err := (&Project{}).Delete(ctx, infer.DeleteRequest[ProjectState]{
		ID: "project-1", State: response.Output,
	}); err != nil {
		t.Fatalf("idempotent project delete: %v", err)
	}
}

func TestCustomDomainUpdateClearsTargetPortAndDeleteIsIdempotent(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			if targetPort, exists := request.Variables["targetPort"]; !exists || targetPort != nil {
				t.Errorf("targetPort = %#v, exists=%v; want explicit null", targetPort, exists)
			}
			writeGraphQL(t, w, map[string]interface{}{"customDomainUpdate": true})
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"message":"Domain not found","extensions":{"code":"NOT_FOUND"}}]}`))
		default:
			t.Fatalf("unexpected request")
		}
	}))
	defer server.Close()
	port := 8080
	args := CustomDomainArgs{
		ProjectID: "project-1", EnvironmentID: "environment-1", ServiceID: "service-1",
		Domain: "api.example.com", TargetPort: &port,
	}
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&CustomDomainResource{}).Update(
		ctx,
		infer.UpdateRequest[CustomDomainArgs, CustomDomainState]{
			ID: "domain-1", State: CustomDomainState{CustomDomainArgs: args, RailwayID: "domain-1"},
			Inputs: CustomDomainArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1",
				ServiceID: "service-1", Domain: "api.example.com",
			},
		},
	)
	if err != nil {
		t.Fatalf("update custom domain: %v", err)
	}
	if response.Output.TargetPort != nil {
		t.Fatalf("target port remained in state: %#v", response.Output)
	}
	if _, err := (&CustomDomainResource{}).Delete(
		ctx,
		infer.DeleteRequest[CustomDomainState]{ID: "domain-1", State: response.Output},
	); err != nil {
		t.Fatalf("idempotent custom domain delete: %v", err)
	}
}

func TestVariableUpdateAndDeleteUseRailwayNameField(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		input := request.Variables["input"].(map[string]interface{})
		if input["name"] != "NODE_ENV" {
			t.Errorf("name = %#v, want NODE_ENV", input["name"])
		}
		if _, exists := input["key"]; exists {
			t.Errorf("mutation used key instead of name: %#v", input)
		}
		if calls.Add(1) == 1 {
			writeGraphQL(t, w, map[string]interface{}{"variableUpsert": true})
		} else {
			writeGraphQL(t, w, map[string]interface{}{"variableDelete": true})
		}
	}))
	defer server.Close()
	state := VariableState{
		VariableArgs: VariableArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1",
			Key: "NODE_ENV", Value: "staging",
		},
		RailwayID: "project-1/environment-1//NODE_ENV",
	}
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Variable{}).Update(ctx, infer.UpdateRequest[VariableArgs, VariableState]{
		ID: state.RailwayID, State: state,
		Inputs: VariableArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1",
			Key: "NODE_ENV", Value: "production",
		},
	})
	if err != nil {
		t.Fatalf("update variable: %v", err)
	}
	if _, err := (&Variable{}).Delete(ctx, infer.DeleteRequest[VariableState]{
		ID: state.RailwayID, State: response.Output,
	}); err != nil {
		t.Fatalf("delete variable: %v", err)
	}
}

func TestVolumeUpdateClearsNameChangesMountAndDeleteIsIdempotent(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			input := request.Variables["input"].(map[string]interface{})
			if name, exists := input["name"]; !exists || name != nil {
				t.Errorf("name = %#v, exists=%v; want explicit null", name, exists)
			}
			writeGraphQL(t, w, map[string]interface{}{"volumeUpdate": map[string]interface{}{"id": "volume-1"}})
		case 2:
			input := request.Variables["input"].(map[string]interface{})
			if input["mountPath"] != "/cache" {
				t.Errorf("mountPath = %#v, want /cache", input["mountPath"])
			}
			writeGraphQL(t, w, map[string]interface{}{"volumeInstanceUpdate": true})
		case 3:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"message":"Volume not found","extensions":{"code":"NOT_FOUND"}}]}`))
		default:
			t.Fatalf("unexpected request")
		}
	}))
	defer server.Close()
	name := "data"
	state := VolumeState{
		VolumeArgs: VolumeArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", ServiceID: "service-1",
			MountPath: "/data", Name: &name,
		},
		RailwayID: "volume-1",
	}
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Volume{}).Update(ctx, infer.UpdateRequest[VolumeArgs, VolumeState]{
		ID: "volume-1", State: state,
		Inputs: VolumeArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1",
			ServiceID: "service-1", MountPath: "/cache",
		},
	})
	if err != nil {
		t.Fatalf("update volume: %v", err)
	}
	if _, err := (&Volume{}).Delete(ctx, infer.DeleteRequest[VolumeState]{
		ID: "volume-1", State: response.Output,
	}); err != nil {
		t.Fatalf("idempotent volume delete: %v", err)
	}
}

func TestServiceCheckSkipsUnknownReplicaCount(t *testing.T) {
	t.Parallel()
	inputs := property.NewMap(map[string]property.Value{
		"projectId":     property.New("project-1"),
		"environmentId": property.New("environment-1"),
		"name":          property.New("web"),
		"numReplicas":   property.New(property.Computed),
	})
	response, err := (&Service{}).Check(t.Context(), infer.CheckRequest{Name: "web", NewInputs: inputs})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(response.Failures) != 0 {
		t.Fatalf("unknown numReplicas must not fail validation: %#v", response.Failures)
	}
}

func TestCustomDomainCheckSkipsUnknownTargetPort(t *testing.T) {
	t.Parallel()
	inputs := property.NewMap(map[string]property.Value{
		"projectId":     property.New("project-1"),
		"environmentId": property.New("environment-1"),
		"serviceId":     property.New("service-1"),
		"domain":        property.New("api.example.com"),
		"targetPort":    property.New(property.Computed),
	})
	response, err := (&CustomDomainResource{}).Check(t.Context(), infer.CheckRequest{
		Name: "api", NewInputs: inputs,
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(response.Failures) != 0 {
		t.Fatalf("unknown targetPort must not fail validation: %#v", response.Failures)
	}
}

func TestServiceCheckSkipsOptionalValidationForUnknownSource(t *testing.T) {
	t.Parallel()
	inputs := property.NewMap(map[string]property.Value{
		"projectId":     property.New("project-1"),
		"environmentId": property.New("environment-1"),
		"name":          property.New("web"),
		"image":         property.New(property.Computed),
		"repo":          property.New("owner/repo"),
	})
	response, err := (&Service{}).Check(t.Context(), infer.CheckRequest{Name: "web", NewInputs: inputs})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(response.Failures) != 0 {
		t.Fatalf("unknown image must not conflict with repo during preview: %#v", response.Failures)
	}
}

func TestCheckSkipsRequiredValidationForUnknownValues(t *testing.T) {
	t.Parallel()
	inputs := property.NewMap(map[string]property.Value{
		"projectId":     property.New("project-1"),
		"environmentId": property.New(property.Computed), // output of another resource at preview time
		"name":          property.New("web"),
	})
	response, err := (&Service{}).Check(t.Context(), infer.CheckRequest{Name: "web", NewInputs: inputs})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(response.Failures) != 0 {
		t.Fatalf("unknown environmentId must not fail validation: %#v", response.Failures)
	}
}

func TestCheckFailsWhenRequiredValueIsEmpty(t *testing.T) {
	t.Parallel()
	inputs := property.NewMap(map[string]property.Value{
		"projectId":     property.New("project-1"),
		"environmentId": property.New(""),
		"name":          property.New("web"),
	})
	response, err := (&Service{}).Check(t.Context(), infer.CheckRequest{Name: "web", NewInputs: inputs})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(response.Failures) != 1 || response.Failures[0].Property != "environmentId" {
		t.Fatalf("empty environmentId must fail validation: %#v", response.Failures)
	}
}

// --- Environment ---

func TestEnvironmentCreateIssuesGraphQLCreate(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(request.Query, "environmentCreate") {
			t.Errorf("expected environmentCreate, got %s", request.Query)
		}
		input, _ := request.Variables["input"].(map[string]interface{})
		if input["projectId"] != "project-1" || input["name"] != "staging" {
			t.Errorf("environmentCreate input = %#v", input)
		}
		writeGraphQL(t, w, map[string]interface{}{
			"environmentCreate": map[string]interface{}{"id": "environment-2", "name": "staging"},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Environment{}).Create(ctx, infer.CreateRequest[EnvironmentArgs]{
		Name:   "staging",
		Inputs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"},
	})
	if err != nil {
		t.Fatalf("environment create failed: %v", err)
	}
	if response.ID != "environment-2" {
		t.Fatalf("environment create ID = %q, want environment-2", response.ID)
	}
	if response.Output.RailwayID != "environment-2" || response.Output.Name != "staging" {
		t.Fatalf("environment create output = %#v", response.Output)
	}
}

func TestEnvironmentCreateFailsOnEmptyID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGraphQL(t, w, map[string]interface{}{
			"environmentCreate": map[string]interface{}{"id": "", "name": "staging"},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Environment{}).Create(ctx, infer.CreateRequest[EnvironmentArgs]{
		Name:   "staging",
		Inputs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"},
	})
	if err == nil {
		t.Fatal("expected empty-ID create to fail")
	}
}

func TestEnvironmentUpdateRenames(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(request.Query, "environmentRename") {
			t.Errorf("expected environmentRename, got %s", request.Query)
		}
		// environmentRename returns Environment!, so the mutation must
		// select a subselection (a bare scalar shape is invalid GraphQL).
		if !strings.Contains(request.Query, "environmentRename(id: $id, input: $input) { id }") {
			t.Errorf("environmentRename must select fields from the returned Environment: %s", request.Query)
		}
		if request.Variables["id"] != "environment-2" {
			t.Errorf("environmentRename id = %#v", request.Variables["id"])
		}
		input, _ := request.Variables["input"].(map[string]interface{})
		if input["name"] != "production" {
			t.Errorf("environmentRename input = %#v", input)
		}
		writeGraphQL(t, w, map[string]interface{}{
			"environmentRename": map[string]interface{}{"id": "environment-2"},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Environment{}).Update(ctx, infer.UpdateRequest[EnvironmentArgs, EnvironmentState]{
		ID:     "environment-2",
		State:  EnvironmentState{EnvironmentArgs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"}, RailwayID: "environment-2"},
		Inputs: EnvironmentArgs{ProjectID: "project-1", Name: "production"},
	})
	if err != nil {
		t.Fatalf("environment rename failed: %v", err)
	}
	if response.Output.Name != "production" {
		t.Fatalf("environment rename output = %#v", response.Output)
	}
}

func TestEnvironmentUpdateSkipsAPIWhenNameUnchanged(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("update with unchanged name must not call the API")
		writeGraphQL(t, w, map[string]interface{}{})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Environment{}).Update(ctx, infer.UpdateRequest[EnvironmentArgs, EnvironmentState]{
		ID:     "environment-2",
		State:  EnvironmentState{EnvironmentArgs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"}, RailwayID: "environment-2"},
		Inputs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"},
	})
	if err != nil {
		t.Fatalf("environment no-op update failed: %v", err)
	}
}

func TestEnvironmentReadPopulatesStateFromAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeGraphQL(t, w, map[string]interface{}{
			"environment": map[string]interface{}{
				"id": "environment-2", "name": "staging", "projectId": "project-1",
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Environment{}).Read(ctx, infer.ReadRequest[EnvironmentArgs, EnvironmentState]{
		ID:     "environment-2",
		Inputs: EnvironmentArgs{ProjectID: "project-1", Name: "old-name"},
		State:  EnvironmentState{RailwayID: "environment-2"},
	})
	if err != nil {
		t.Fatalf("environment read failed: %v", err)
	}
	if response.State.Name != "staging" || response.State.ProjectID != "project-1" {
		t.Fatalf("environment read state = %#v", response.State)
	}
	if response.Inputs.Name != "staging" {
		t.Fatalf("environment read inputs = %#v", response.Inputs)
	}
}

func TestEnvironmentReadReturnsEmptyWhenAPIReturnsNullEnvironment(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGraphQL(t, w, map[string]interface{}{"environment": nil})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Environment{}).Read(ctx, infer.ReadRequest[EnvironmentArgs, EnvironmentState]{
		ID:     "environment-2",
		Inputs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"},
		State:  EnvironmentState{RailwayID: "environment-2"},
	})
	if err != nil {
		t.Fatalf("environment read failed: %v", err)
	}
	if response.ID != "" || response.State.RailwayID != "" {
		t.Fatalf("environment read should be empty when the environment is gone, got %#v", response)
	}
}

func TestEnvironmentDeleteIsIdempotent(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writeGraphQL(t, w, map[string]interface{}{
			"errors": []map[string]interface{}{
				{"message": "environment not found", "extensions": map[string]interface{}{"code": "NOT_FOUND"}},
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Environment{}).Delete(ctx, infer.DeleteRequest[EnvironmentState]{
		ID: "environment-2",
		State: EnvironmentState{
			EnvironmentArgs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"},
			RailwayID:       "environment-2",
		},
	})
	if err != nil {
		t.Fatalf("environment delete should be idempotent, got %v", introspectErr(err))
	}
}

func introspectErr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestEnvironmentImportParsesCompositeID(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Variables["id"] != "environment-2" {
			t.Errorf("environment id = %#v, want environment-2", request.Variables["id"])
		}
		writeGraphQL(t, w, map[string]interface{}{
			"environment": map[string]interface{}{
				"id": "environment-2", "name": "staging", "projectId": "project-1",
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Environment{}).Read(ctx, infer.ReadRequest[EnvironmentArgs, EnvironmentState]{
		ID: "project-1/environment-2",
	})
	if err != nil {
		t.Fatalf("import read failed: %v", err)
	}
	if response.ID != "environment-2" || response.Inputs.ProjectID != "project-1" || response.Inputs.Name != "staging" {
		t.Fatalf("imported environment = id=%q inputs=%#v", response.ID, response.Inputs)
	}
}

func TestEnvironmentUpdateRenameFailurePropagates(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"environment name already exists"}]}`))
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Environment{}).Update(ctx, infer.UpdateRequest[EnvironmentArgs, EnvironmentState]{
		ID:     "environment-2",
		State:  EnvironmentState{EnvironmentArgs: EnvironmentArgs{ProjectID: "project-1", Name: "staging"}, RailwayID: "environment-2"},
		Inputs: EnvironmentArgs{ProjectID: "project-1", Name: "production"},
	})
	if err == nil || !strings.Contains(err.Error(), "rename environment") {
		t.Fatalf("rename failure must propagate, got %v", err)
	}
}

// --- Bucket ---

func TestBucketCreateCreatesDeploysAndFetchesCredentials(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(request.Query, "bucketCreate") {
				t.Errorf("expected bucketCreate, got %s", request.Query)
			}
			input, _ := request.Variables["input"].(map[string]interface{})
			if input["projectId"] != "project-1" {
				t.Errorf("bucketCreate input = %#v", input)
			}
			if _, exists := input["name"]; exists {
				t.Errorf("bucketCreate must omit name when unset: %#v", input)
			}
			writeGraphQL(t, w, map[string]interface{}{
				"bucketCreate": map[string]interface{}{"id": "bucket-1", "name": "uploads-abc123"},
			})
		case 2:
			if !strings.Contains(request.Query, "environmentPatchCommit") {
				t.Errorf("expected environmentPatchCommit, got %s", request.Query)
			}
			if request.Variables["environmentId"] != "environment-1" {
				t.Errorf("patch environmentId = %#v", request.Variables["environmentId"])
			}
			patch, _ := request.Variables["patch"].(map[string]interface{})
			buckets, _ := patch["buckets"].(map[string]interface{})
			instance, _ := buckets["bucket-1"].(map[string]interface{})
			if instance["region"] != "sjc" || instance["isCreated"] != true {
				t.Errorf("patch buckets.bucket-1 = %#v", instance)
			}
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
		case 3:
			if !strings.Contains(request.Query, "bucketS3Credentials") {
				t.Errorf("expected bucketS3Credentials, got %s", request.Query)
			}
			writeGraphQL(t, w, map[string]interface{}{
				"bucketS3Credentials": []map[string]interface{}{
					{
						"accessKeyId":     "key-1",
						"secretAccessKey": "secret-1",
						"endpoint":        "https://storage.railway.app",
						"bucketName":      "uploads-abc123",
						"region":          "auto",
						"urlStyle":        "virtual",
					},
				},
			})
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Create(ctx, infer.CreateRequest[BucketArgs]{
		Name:   "uploads",
		Inputs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
	})
	if err != nil {
		t.Fatalf("bucket create failed: %v", err)
	}
	if response.ID != "bucket-1" {
		t.Fatalf("bucket create ID = %q, want bucket-1", response.ID)
	}
	out := response.Output
	if out.Name == nil || *out.Name != "uploads-abc123" {
		t.Fatalf("bucket create name = %#v", out.Name)
	}
	if out.AccessKeyID != "key-1" || out.SecretAccessKey != "secret-1" ||
		out.Endpoint != "https://storage.railway.app" || out.S3BucketName != "uploads-abc123" ||
		out.S3Region != "auto" || out.URLStyle != "virtual" {
		t.Fatalf("bucket create credentials = %#v", out)
	}
}

func TestBucketCreatePartialStateWhenDeployFails(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			writeGraphQL(t, w, map[string]interface{}{
				"bucketCreate": map[string]interface{}{"id": "bucket-1", "name": "uploads"},
			})
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"errors":[{"message":"environment has staged changes"}]}`))
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Create(ctx, infer.CreateRequest[BucketArgs]{
		Name:   "uploads",
		Inputs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "iad"},
	})
	var initErr infer.ResourceInitFailedError
	if !errors.As(err, &initErr) {
		t.Fatalf("expected ResourceInitFailedError, got %v", err)
	}
	if response.ID != "bucket-1" || response.Output.RailwayID != "bucket-1" {
		t.Fatalf("partial response did not preserve bucket ID: %#v", response)
	}
}

func TestBucketDeletePatchesWithIsDeleted(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "environmentPatchCommit") {
			patch, _ := request.Variables["patch"].(map[string]interface{})
			buckets, _ := patch["buckets"].(map[string]interface{})
			instance, _ := buckets["bucket-1"].(map[string]interface{})
			if instance["isDeleted"] != true {
				t.Errorf("delete patch buckets.bucket-1 = %#v", instance)
			}
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
			return
		}
		if strings.Contains(request.Query, "projectBuckets") {
			// Read-back after deletion: bucket is gone from the project.
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"buckets": map[string]interface{}{
						"edges":    []map[string]interface{}{},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
			return
		}
		t.Errorf("unexpected API call: %s", request.Query)
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Bucket{}).Delete(ctx, infer.DeleteRequest[BucketState]{
		ID: "bucket-1",
		State: BucketState{
			BucketArgs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
			RailwayID:  "bucket-1",
		},
	})
	if err != nil {
		t.Fatalf("bucket delete failed: %v", err)
	}
}

func TestBucketUpdateRenamesAndRedeploys(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(request.Query, "bucketUpdate") {
				t.Errorf("expected bucketUpdate, got %s", request.Query)
			}
			input, _ := request.Variables["input"].(map[string]interface{})
			if input["name"] != "assets" {
				t.Errorf("bucketUpdate input = %#v", input)
			}
			writeGraphQL(t, w, map[string]interface{}{
				"bucketUpdate": map[string]interface{}{"id": "bucket-1", "name": "assets"},
			})
		case 2:
			if !strings.Contains(request.Query, "environmentPatchCommit") {
				t.Errorf("expected environmentPatchCommit, got %s", request.Query)
			}
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
		case 3:
			writeGraphQL(t, w, map[string]interface{}{
				"bucketS3Credentials": []map[string]interface{}{
					{
						"accessKeyId": "key-1", "secretAccessKey": "secret-1",
						"endpoint": "https://storage.railway.app", "bucketName": "assets",
						"region": "auto", "urlStyle": "virtual",
					},
				},
			})
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	oldName := "uploads"
	newName := "assets"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Update(ctx, infer.UpdateRequest[BucketArgs, BucketState]{
		ID: "bucket-1",
		State: BucketState{
			BucketArgs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc", Name: &oldName},
			RailwayID:  "bucket-1",
		},
		Inputs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc", Name: &newName},
	})
	if err != nil {
		t.Fatalf("bucket update failed: %v", err)
	}
	if response.Output.Name == nil || *response.Output.Name != "assets" {
		t.Fatalf("bucket update name = %#v", response.Output.Name)
	}
}

func TestBucketReadResolvesProjectBucketAndRegion(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"buckets": map[string]interface{}{
						"edges": []map[string]interface{}{
							{"node": map[string]interface{}{"id": "bucket-1", "name": "uploads"}},
						},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
		case 2:
			config := `{"buckets": {"bucket-1": {"region": "sjc", "isCreated": true}}}`
			writeGraphQL(t, w, map[string]interface{}{
				"environment": map[string]interface{}{"config": json.RawMessage(config)},
			})
		case 3:
			writeGraphQL(t, w, map[string]interface{}{
				"bucketS3Credentials": []map[string]interface{}{
					{
						"accessKeyId": "key-1", "secretAccessKey": "secret-1",
						"endpoint": "https://storage.railway.app", "bucketName": "uploads",
						"region": "auto", "urlStyle": "virtual",
					},
				},
			})
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Read(ctx, infer.ReadRequest[BucketArgs, BucketState]{
		ID:     "bucket-1",
		Inputs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
		State:  BucketState{RailwayID: "bucket-1"},
	})
	if err != nil {
		t.Fatalf("bucket read failed: %v", err)
	}
	if response.State.Region != "sjc" || response.State.Name == nil || *response.State.Name != "uploads" {
		t.Fatalf("bucket read state = %#v", response.State)
	}
}

func TestBucketReadKeepsUndeployedProjectBucket(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"buckets": map[string]interface{}{
						"edges": []map[string]interface{}{
							{"node": map[string]interface{}{"id": "bucket-1", "name": "uploads"}},
						},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
		case 2:
			// Bucket exists at project level but is not deployed to the env.
			config := `{}`
			writeGraphQL(t, w, map[string]interface{}{
				"environment": map[string]interface{}{"config": json.RawMessage(config)},
			})
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Read(ctx, infer.ReadRequest[BucketArgs, BucketState]{
		ID:     "bucket-1",
		Inputs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
		State:  BucketState{RailwayID: "bucket-1"},
	})
	if err != nil {
		t.Fatalf("bucket read failed: %v", err)
	}
	if response.ID != "bucket-1" {
		t.Fatalf("undeployed project bucket must stay in state, got %#v", response)
	}
	if response.State.Region != "sjc" || response.State.Name == nil || *response.State.Name != "uploads" {
		t.Fatalf("undeployed bucket state = %#v", response.State)
	}
	if response.State.AccessKeyID != "" {
		t.Fatalf("undeployed bucket must not invent credentials: %#v", response.State)
	}
}

func TestBucketReadReturnsEmptyWhenProjectBucketMissing(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(request.Query, "projectBuckets") {
			t.Errorf("unexpected API call: %s", request.Query)
		}
		writeGraphQL(t, w, map[string]interface{}{
			"project": map[string]interface{}{
				"buckets": map[string]interface{}{
					"edges":    []map[string]interface{}{},
					"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
				},
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Read(ctx, infer.ReadRequest[BucketArgs, BucketState]{
		ID:     "bucket-1",
		Inputs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
		State:  BucketState{RailwayID: "bucket-1"},
	})
	if err != nil {
		t.Fatalf("bucket read failed: %v", err)
	}
	if response.ID != "" {
		t.Fatalf("missing project bucket must read as gone, got %#v", response)
	}
}

func TestBucketCheckRejectsInvalidRegion(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("check must not call the API")
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Check(ctx, infer.CheckRequest{
		NewInputs: property.NewMap(map[string]property.Value{
			"projectId":     property.New("project-1"),
			"environmentId": property.New("environment-1"),
			"region":        property.New("us-east-1"),
		}),
	})
	if err != nil {
		t.Fatalf("bucket check failed: %v", err)
	}
	found := false
	for _, failure := range response.Failures {
		if failure.Property == "region" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a region failure, got %#v", response.Failures)
	}
}

func TestBucketDeleteFailsWhenBucketStillExistsAfterPatch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "environmentPatchCommit") {
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
			return
		}
		if strings.Contains(request.Query, "projectBuckets") {
			// Staged changes: the patch was acked but the bucket persists.
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"buckets": map[string]interface{}{
						"edges": []map[string]interface{}{
							{"node": map[string]interface{}{"id": "bucket-1", "name": "uploads"}},
						},
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
			return
		}
		t.Errorf("unexpected API call: %s", request.Query)
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Bucket{}).Delete(ctx, infer.DeleteRequest[BucketState]{
		ID: "bucket-1",
		State: BucketState{
			BucketArgs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
			RailwayID:  "bucket-1",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("delete must fail loudly when the bucket persists, got %v", err)
	}
}

func TestBucketDeleteRetriesUntilBucketIsGone(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "environmentPatchCommit") {
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
			return
		}
		if strings.Contains(request.Query, "projectBuckets") {
			edges := []map[string]interface{}{}
			if reads.Add(1) == 1 {
				edges = []map[string]interface{}{
					{"node": map[string]interface{}{"id": "bucket-1", "name": "uploads"}},
				}
			}
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"buckets": map[string]interface{}{
						"edges":    edges,
						"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
					},
				},
			})
			return
		}
		t.Errorf("unexpected API call: %s", request.Query)
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Bucket{}).Delete(ctx, infer.DeleteRequest[BucketState]{
		ID: "bucket-1",
		State: BucketState{
			BucketArgs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
			RailwayID:  "bucket-1",
		},
	})
	if err != nil {
		t.Fatalf("delete should succeed after the bucket disappears, got %v", err)
	}
	if reads.Load() < 2 {
		t.Fatalf("delete must retry verification when the bucket still exists, got %d reads", reads.Load())
	}
}

func TestBucketReadPaginatesProjectBuckets(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "environmentConfig") {
			config := `{"buckets": {"bucket-1": {"region": "sjc", "isCreated": true}}}`
			writeGraphQL(t, w, map[string]interface{}{
				"environment": map[string]interface{}{"config": json.RawMessage(config)},
			})
			return
		}
		if strings.Contains(request.Query, "bucketS3Credentials") {
			writeGraphQL(t, w, map[string]interface{}{
				"bucketS3Credentials": []map[string]interface{}{
					{
						"accessKeyId": "key-1", "secretAccessKey": "secret-1",
						"endpoint": "https://storage.railway.app", "bucketName": "uploads",
						"region": "auto", "urlStyle": "virtual",
					},
				},
			})
			return
		}
		if request.Variables["after"] == nil {
			writeGraphQL(t, w, map[string]interface{}{
				"project": map[string]interface{}{
					"buckets": map[string]interface{}{
						"edges":    []map[string]interface{}{},
						"pageInfo": map[string]interface{}{"hasNextPage": true, "endCursor": "buckets-1"},
					},
				},
			})
			return
		}
		writeGraphQL(t, w, map[string]interface{}{
			"project": map[string]interface{}{
				"buckets": map[string]interface{}{
					"edges": []map[string]interface{}{
						{"node": map[string]interface{}{"id": "bucket-1", "name": "uploads"}},
					},
					"pageInfo": map[string]interface{}{"hasNextPage": false, "endCursor": ""},
				},
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Bucket{}).Read(ctx, infer.ReadRequest[BucketArgs, BucketState]{
		ID:     "bucket-1",
		Inputs: BucketArgs{ProjectID: "project-1", EnvironmentID: "environment-1", Region: "sjc"},
		State:  BucketState{RailwayID: "bucket-1"},
	})
	if err != nil {
		t.Fatalf("paginated bucket read failed: %v", err)
	}
	if response.State.Name == nil || *response.State.Name != "uploads" {
		t.Fatalf("bucket after pagination was not found: %#v", response.State)
	}
}

// --- Service upgrades (healthcheckTimeout, in-place image, replicas 0, autoUpdates) ---

func TestServiceUpdateChangesImageInPlace(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "serviceDelete") {
			t.Errorf("image change must not delete the service")
		}
		if strings.Contains(request.Query, "serviceInstanceUpdate") {
			input, _ := request.Variables["input"].(map[string]interface{})
			source, _ := input["source"].(map[string]interface{})
			if source == nil || source["image"] != "ghcr.io/acme/web@sha256:new" {
				t.Errorf("instance update source = %#v", input)
			}
			writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
			return
		}
		writeGraphQL(t, w, map[string]interface{}{})
	}))
	defer server.Close()

	oldImage := "ghcr.io/acme/web@sha256:old"
	newImage := "ghcr.io/acme/web@sha256:new"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		State: ServiceState{
			ServiceArgs: ServiceArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", Image: &oldImage,
			},
			RailwayID: "service-1", InstanceID: "instance-1",
		},
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", Image: &newImage,
		},
	})
	if err != nil {
		t.Fatalf("in-place image update failed: %v", err)
	}
	if response.Output.Image == nil || *response.Output.Image != newImage {
		t.Fatalf("update output image = %#v", response.Output.Image)
	}
}

func TestServiceUpdateUnchangedImageSendsNoSource(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "serviceInstanceUpdate") {
			input, _ := request.Variables["input"].(map[string]interface{})
			if _, exists := input["source"]; exists {
				t.Errorf("unchanged image must not resend source: %#v", input)
			}
			writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
			return
		}
		t.Errorf("unexpected API call: %s", request.Query)
	}))
	defer server.Close()

	image := "ghcr.io/acme/web@sha256:pin"
	start := "npm start"
	old := ServiceArgs{
		ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", Image: &image, StartCommand: &start,
	}
	next := ServiceArgs{
		ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", Image: &image, StartCommand: &start,
	}
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1", State: ServiceState{
			ServiceArgs: old, RailwayID: "service-1", InstanceID: "instance-1",
		}, Inputs: next,
	})
	if err != nil {
		t.Fatalf("no-op update failed: %v", err)
	}
}

func TestServiceUpdateScalesToZeroReplicas(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "serviceInstanceUpdate") {
			input, _ := request.Variables["input"].(map[string]interface{})
			if value, ok := input["numReplicas"].(float64); !ok || value != 0 {
				t.Errorf("numReplicas = %#v, want 0", input["numReplicas"])
			}
			writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
			return
		}
		writeGraphQL(t, w, map[string]interface{}{})
	}))
	defer server.Close()

	one, zero := 1, 0
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		State: ServiceState{
			ServiceArgs: ServiceArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", NumReplicas: &one,
			},
			RailwayID: "service-1", InstanceID: "instance-1",
		},
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", NumReplicas: &zero,
		},
	})
	if err != nil {
		t.Fatalf("scale-to-zero failed: %v", err)
	}
}

func TestServiceUpdatePatchesAutoUpdatePolicy(t *testing.T) {
	t.Parallel()
	sawInstanceUpdate := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "environmentPatchCommit") {
			patch, _ := request.Variables["patch"].(map[string]interface{})
			services, _ := patch["services"].(map[string]interface{})
			instance, _ := services["service-1"].(map[string]interface{})
			source, _ := instance["source"].(map[string]interface{})
			autoUpdates, _ := source["autoUpdates"].(map[string]interface{})
			if autoUpdates["type"] != "patch" {
				t.Errorf("autoUpdates patch = %#v", patch)
			}
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
			return
		}
		if strings.Contains(request.Query, "serviceInstanceUpdate") {
			sawInstanceUpdate = true
			writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
			return
		}
		t.Errorf("unexpected API call: %s", request.Query)
	}))
	defer server.Close()

	image := "gotenberg/gotenberg:8"
	start := "gotenberg --api-port=3000"
	patchType := "patch"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		State: ServiceState{
			ServiceArgs: ServiceArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1", Name: "gotenberg", Image: &image,
			},
			RailwayID: "service-1", InstanceID: "instance-1",
		},
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "gotenberg", Image: &image,
			StartCommand: &start, AutoUpdateType: &patchType,
		},
	})
	if err != nil {
		t.Fatalf("auto-update patch failed: %v", err)
	}
	if !sawInstanceUpdate {
		t.Error("expected a serviceInstanceUpdate for the changed start command")
	}
}

func TestServiceUpdateClearsAutoUpdatePolicy(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "environmentPatchCommit") {
			patch, _ := request.Variables["patch"].(map[string]interface{})
			services, _ := patch["services"].(map[string]interface{})
			instance, _ := services["service-1"].(map[string]interface{})
			source, _ := instance["source"].(map[string]interface{})
			autoUpdates, _ := source["autoUpdates"].(map[string]interface{})
			if autoUpdates["type"] != "disabled" {
				t.Errorf("clear autoUpdates patch = %#v", patch)
			}
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
			return
		}
		t.Errorf("unexpected API call: %s", request.Query)
	}))
	defer server.Close()

	image := "gotenberg/gotenberg:8"
	oldType := "patch"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		State: ServiceState{
			ServiceArgs: ServiceArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1", Name: "gotenberg", Image: &image, AutoUpdateType: &oldType,
			},
			RailwayID: "service-1", InstanceID: "instance-1",
		},
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "gotenberg", Image: &image,
		},
	})
	if err != nil {
		t.Fatalf("auto-update clear failed: %v", err)
	}
}

func TestServiceCreateWithAutoUpdatePolicy(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch calls.Add(1) {
		case 1:
			writeGraphQL(t, w, map[string]interface{}{
				"serviceCreate": map[string]interface{}{"id": "service-1", "name": "gotenberg"},
			})
		case 2:
			writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
		case 3:
			if !strings.Contains(request.Query, "environmentPatchCommit") {
				t.Errorf("expected environmentPatchCommit, got %s", request.Query)
			}
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
		case 4:
			writeGraphQL(t, w, map[string]interface{}{
				"serviceInstance": map[string]interface{}{"id": "instance-1"},
			})
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}))
	defer server.Close()

	image := "gotenberg/gotenberg:8"
	minor := "minor"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Service{}).Create(ctx, infer.CreateRequest[ServiceArgs]{
		Name: "gotenberg",
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "gotenberg",
			Image: &image, AutoUpdateType: &minor,
		},
	})
	if err != nil {
		t.Fatalf("service create with auto-update failed: %v", err)
	}
	if response.Output.AutoUpdateType == nil || *response.Output.AutoUpdateType != "minor" {
		t.Fatalf("create output autoUpdateType = %#v", response.Output.AutoUpdateType)
	}
}

func TestServiceUpdateSendsHealthcheckTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "serviceInstanceUpdate") {
			input, _ := request.Variables["input"].(map[string]interface{})
			if value, ok := input["healthcheckTimeout"].(float64); !ok || value != 30 {
				t.Errorf("healthcheckTimeout = %#v, want 30", input["healthcheckTimeout"])
			}
			writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
			return
		}
		writeGraphQL(t, w, map[string]interface{}{})
	}))
	defer server.Close()

	oldTimeout, newTimeout := 300, 30
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		State: ServiceState{
			ServiceArgs: ServiceArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", HealthcheckTimeout: &oldTimeout,
			},
			RailwayID: "service-1", InstanceID: "instance-1",
		},
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", HealthcheckTimeout: &newTimeout,
		},
	})
	if err != nil {
		t.Fatalf("healthcheck timeout update failed: %v", err)
	}
}

func TestServiceReadPopulatesAutoUpdatePolicyFromEnvironmentConfig(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "query serviceInstance") {
			// ServiceSource exposes only image and repo; the policy lives
			// in the environment config.
			writeGraphQL(t, w, map[string]interface{}{
				"serviceInstance": map[string]interface{}{
					"id":                 "instance-1",
					"healthcheckTimeout": 60,
					"source": map[string]interface{}{
						"image": "gotenberg/gotenberg:8",
					},
				},
			})
			return
		}
		if strings.Contains(request.Query, "environmentConfig") {
			config := `{"services": {"service-1": {"source": {"image": "gotenberg/gotenberg:8", "autoUpdates": {"type": "patch"}}}}}`
			writeGraphQL(t, w, map[string]interface{}{
				"environment": map[string]interface{}{"config": json.RawMessage(config)},
			})
			return
		}
		writeGraphQL(t, w, map[string]interface{}{
			"service": map[string]interface{}{
				"id": "service-1", "name": "gotenberg", "projectId": "project-1",
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Service{}).Read(ctx, infer.ReadRequest[ServiceArgs, ServiceState]{
		ID:     "service-1",
		Inputs: ServiceArgs{EnvironmentID: "environment-1"},
		State:  ServiceState{RailwayID: "service-1"},
	})
	if err != nil {
		t.Fatalf("service read failed: %v", err)
	}
	if response.State.AutoUpdateType == nil || *response.State.AutoUpdateType != "patch" {
		t.Fatalf("read autoUpdateType = %#v", response.State.AutoUpdateType)
	}
	if response.State.HealthcheckTimeout == nil || *response.State.HealthcheckTimeout != 60 {
		t.Fatalf("read healthcheckTimeout = %#v", response.State.HealthcheckTimeout)
	}
}

func TestServiceReadTreatsDisabledAutoUpdatePolicyAsUnset(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "query serviceInstance") {
			writeGraphQL(t, w, map[string]interface{}{
				"serviceInstance": map[string]interface{}{"id": "instance-1"},
			})
			return
		}
		if strings.Contains(request.Query, "environmentConfig") {
			config := `{"services": {"service-1": {"source": {"autoUpdates": {"type": "disabled"}}}}}`
			writeGraphQL(t, w, map[string]interface{}{
				"environment": map[string]interface{}{"config": json.RawMessage(config)},
			})
			return
		}
		writeGraphQL(t, w, map[string]interface{}{
			"service": map[string]interface{}{
				"id": "service-1", "name": "web", "projectId": "project-1",
			},
		})
	}))
	defer server.Close()

	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Service{}).Read(ctx, infer.ReadRequest[ServiceArgs, ServiceState]{
		ID:     "service-1",
		Inputs: ServiceArgs{EnvironmentID: "environment-1"},
		State:  ServiceState{RailwayID: "service-1"},
	})
	if err != nil {
		t.Fatalf("service read failed: %v", err)
	}
	if response.State.AutoUpdateType != nil {
		t.Fatalf("disabled autoUpdateType must read as unset, got %#v", response.State.AutoUpdateType)
	}
}

func TestServiceCheckNormalizesDisabledAutoUpdateType(t *testing.T) {
	t.Parallel()
	image := property.New("gotenberg/gotenberg:8")
	response, err := (&Service{}).Check(t.Context(), infer.CheckRequest{
		Name: "gotenberg",
		NewInputs: property.NewMap(map[string]property.Value{
			"projectId":      property.New("project-1"),
			"environmentId":  property.New("environment-1"),
			"name":           property.New("gotenberg"),
			"image":          image,
			"autoUpdateType": property.New("disabled"),
		}),
	})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if len(response.Failures) != 0 {
		t.Fatalf("disabled autoUpdateType must be valid: %#v", response.Failures)
	}
	if response.Inputs.AutoUpdateType != nil {
		t.Fatalf("disabled autoUpdateType must be stored as unset, got %#v", response.Inputs.AutoUpdateType)
	}
}

func TestServiceUpdateReappliesConfigurationAfterFailedInit(t *testing.T) {
	t.Parallel()
	var instanceUpdates atomic.Int32
	var policyPatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request capturedGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(request.Query, "serviceInstanceUpdate") {
			input, _ := request.Variables["input"].(map[string]interface{})
			// The failed create never applied the start command, so the
			// convergence update must resend it even though inputs match
			// the (partial) recorded state.
			if input["startCommand"] != "npm start" {
				t.Errorf("convergence update must resend the start command: %#v", input)
			}
			instanceUpdates.Add(1)
			writeGraphQL(t, w, map[string]interface{}{"serviceInstanceUpdate": true})
			return
		}
		if strings.Contains(request.Query, "environmentPatchCommit") {
			policyPatches.Add(1)
			writeGraphQL(t, w, map[string]interface{}{"environmentPatchCommit": true})
			return
		}
		if strings.Contains(request.Query, "query serviceInstance") {
			writeGraphQL(t, w, map[string]interface{}{
				"serviceInstance": map[string]interface{}{"id": "instance-9"},
			})
			return
		}
		t.Errorf("unexpected API call: %s", request.Query)
	}))
	defer server.Close()

	start := "npm start"
	image := "gotenberg/gotenberg:8"
	patchType := "patch"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	response, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		// Partial state from a failed create: no InstanceID, start command
		// and policy present in inputs but never applied server-side.
		State: ServiceState{
			ServiceArgs: ServiceArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1", Name: "gotenberg",
				Image: &image, StartCommand: &start, AutoUpdateType: &patchType,
			},
			RailwayID: "service-1",
		},
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "gotenberg",
			Image: &image, StartCommand: &start, AutoUpdateType: &patchType,
		},
	})
	if err != nil {
		t.Fatalf("convergence update failed: %v", err)
	}
	if instanceUpdates.Load() != 1 {
		t.Errorf("expected exactly one convergence serviceInstanceUpdate, got %d", instanceUpdates.Load())
	}
	if policyPatches.Load() != 1 {
		t.Errorf("expected exactly one policy patch, got %d", policyPatches.Load())
	}
	if response.Output.InstanceID != "instance-9" {
		t.Fatalf("convergence update did not record the instance ID: %#v", response.Output)
	}
}

func TestServiceUpdateRejectsRemovingImage(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("image removal must be rejected before any API call")
	}))
	defer server.Close()

	oldImage := "ghcr.io/acme/web@sha256:old"
	ctx := contextWithClient(t.Context(), newClient(server.URL, "token", accountAuth, server.Client()))
	_, err := (&Service{}).Update(ctx, infer.UpdateRequest[ServiceArgs, ServiceState]{
		ID: "service-1",
		State: ServiceState{
			ServiceArgs: ServiceArgs{
				ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web", Image: &oldImage,
			},
			RailwayID: "service-1", InstanceID: "instance-1",
		},
		Inputs: ServiceArgs{
			ProjectID: "project-1", EnvironmentID: "environment-1", Name: "web",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "removing image is not supported") {
		t.Fatalf("image removal must fail loudly, got %v", err)
	}
}
