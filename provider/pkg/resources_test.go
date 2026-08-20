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
		ID: "service-1", State: ServiceState{ServiceArgs: old, RailwayID: "service-1"}, Inputs: next,
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
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeGraphQL(t, w, map[string]interface{}{
				"service": map[string]interface{}{
					"id": "service-1", "name": "web", "projectId": "project-1", "branch": "main",
				},
			})
		case 2:
			writeGraphQL(t, w, map[string]interface{}{
				"serviceInstance": map[string]interface{}{"id": "instance-1"},
			})
		default:
			t.Fatalf("unexpected request")
		}
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
