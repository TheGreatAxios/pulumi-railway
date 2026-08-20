package pkg

import (
	"encoding/json"
	"strings"
	"testing"

	provider "github.com/pulumi/pulumi-go-provider"
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
			if test.wantErr == "" && test.config.client == nil {
				t.Fatal("Configure did not initialize the shared API client")
			}
			if test.wantErr == "" && strings.TrimSpace(test.config.client.token) != test.config.client.token {
				t.Fatalf("client token was not trimmed: %q", test.config.client.token)
			}
		})
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
