package pkg

import (
	"encoding/json"
	"strings"
	"testing"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

func TestProviderConfigRequiresExactlyOneToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  ProviderConfig
		wantErr string
	}{
		{name: "account token", config: ProviderConfig{Token: "account"}},
		{name: "project token", config: ProviderConfig{ProjectToken: " project "}},
		{name: "neither", wantErr: "configure railway:token or railway:projectToken"},
		{
			name: "both", config: ProviderConfig{Token: "account", ProjectToken: "project"},
			wantErr: "configure only one",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.config.Configure(t.Context())
			if test.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
			if test.wantErr != "" {
				return
			}
			client, err := test.config.apiClient()
			if err != nil {
				t.Fatalf("apiClient after Configure: %v", err)
			}
			if client == nil {
				t.Fatal("Configure did not initialize the shared API client")
			}
			if strings.TrimSpace(client.token) != client.token {
				t.Fatalf("client token was not trimmed: %q", client.token)
			}
		})
	}
}

func TestAPIClientIsSharedPerCredential(t *testing.T) {
	t.Parallel()
	first, err := ProviderConfig{Token: "cache-test-alpha"}.apiClient()
	if err != nil {
		t.Fatalf("apiClient: %v", err)
	}
	again, err := ProviderConfig{Token: " cache-test-alpha "}.apiClient() //nolint:gosec // fake test credential
	if err != nil {
		t.Fatalf("apiClient with untrimmed token: %v", err)
	}
	if first != again {
		t.Error("expected one shared client per credential")
	}
	other, err := ProviderConfig{ProjectToken: "cache-test-beta"}.apiClient()
	if err != nil {
		t.Fatalf("apiClient for project token: %v", err)
	}
	if other == first {
		t.Error("distinct credentials must not share a client")
	}
}

// Regression test: pulumi-go-provider asserts the config VALUE against
// CustomConfigure, so a pointer-receiver Configure would never run and
// resource CRUD would proceed without a configured client.
func TestFrameworkConfigureRunsProviderValidation(t *testing.T) {
	t.Parallel()
	railwayProvider, err := BuildProvider()
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	err = railwayProvider.Configure(t.Context(), provider.ConfigureRequest{
		Args: property.NewMap(map[string]property.Value{
			"token":        property.New("framework-account-token"),
			"projectToken": property.New("framework-project-token"),
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "configure only one") {
		t.Fatalf("Configure error = %v, want the exactly-one-token validation", err)
	}
	// A provider instance is configured once per process; use a fresh one
	// because the framework decodes into a retained config struct.
	freshProvider, err := BuildProvider()
	if err != nil {
		t.Fatalf("build fresh provider: %v", err)
	}
	if err := freshProvider.Configure(t.Context(), provider.ConfigureRequest{
		Args: property.NewMap(map[string]property.Value{
			"token": property.New("framework-account-token"),
		}),
	}); err != nil {
		t.Fatalf("Configure with a single token: %v", err)
	}
}

func TestProviderSchemaUsesPublicPackageShape(t *testing.T) {
	t.Parallel()
	railwayProvider, err := BuildProvider()
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	schema, err := provider.GetSchema(t.Context(), "railway", "0.1.0", railwayProvider)
	if err != nil {
		t.Fatalf("get schema: %v", err)
	}
	if schema.PluginDownloadURL != pluginDownloadURL {
		t.Errorf("pluginDownloadURL = %q, want %q", schema.PluginDownloadURL, pluginDownloadURL)
	}
	if !strings.Contains(string(schema.Language["nodejs"]), `"packageName":"@thegreataxios/pulumi-railway"`) {
		t.Errorf("node package metadata = %s", schema.Language["nodejs"])
	}
	for _, token := range []string{
		"railway:index:Project",
		"railway:index:Service",
		"railway:index:Variable",
		"railway:index:CustomDomain",
		"railway:index:Volume",
	} {
		if _, exists := schema.Resources[token]; !exists {
			t.Errorf("schema is missing %s", token)
		}
	}
	if !schema.Resources["railway:index:Service"].InputProperties["image"].ReplaceOnChanges {
		t.Error("service image must force replacement")
	}
	if !schema.Resources["railway:index:Variable"].InputProperties["key"].ReplaceOnChanges {
		t.Error("variable key must force replacement")
	}
	for token, resource := range schema.Resources {
		if resource.Description == "" {
			t.Errorf("%s has no resource description", token)
		}
		for name, property := range resource.InputProperties {
			if property.Description == "" {
				t.Errorf("%s input %s has no description", token, name)
			}
		}
		for name, property := range resource.Properties {
			if property.Description == "" {
				t.Errorf("%s property %s has no description", token, name)
			}
		}
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	if strings.Contains(string(encoded), `"default":""`) {
		t.Error("optional token configuration must not use an empty-string default")
	}
	domainAliases := schema.Resources["railway:index:CustomDomain"].Aliases
	var singular, plural bool
	for _, alias := range domainAliases {
		singular = singular || alias.Type == "railway:pkg:CustomDomain"
		plural = plural || alias.Type == "railway:pkg:CustomDomains"
	}
	if !singular || !plural {
		t.Errorf("custom domain aliases missing: %#v", domainAliases)
	}
}
