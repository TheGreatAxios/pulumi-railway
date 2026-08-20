package pkg

import (
	"context"
	"errors"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

const pluginDownloadURL = "github://api.github.com/thegreataxios/pulumi-railway"

// ProviderConfig controls Railway API authentication. Set exactly one token.
type ProviderConfig struct {
	// Railway account or workspace API token.
	Token string `pulumi:"token,optional" provider:"secret"`
	// Railway project token.
	ProjectToken string `pulumi:"projectToken,optional" provider:"secret"`
	client       *Client
}

func (c *ProviderConfig) Annotate(a infer.Annotator) {
	a.Describe(c, "Configuration for authenticating with the Railway API.")
	a.Describe(&c.Token, "Railway account or workspace API token.")
	a.Describe(&c.ProjectToken, "Railway project token.")
	a.SetDefault(&c.Token, nil, "RAILWAY_API_TOKEN")
	a.SetDefault(&c.ProjectToken, nil, "RAILWAY_TOKEN")
}

func (c *ProviderConfig) Configure(context.Context) error {
	c.Token = strings.TrimSpace(c.Token)
	c.ProjectToken = strings.TrimSpace(c.ProjectToken)
	hasAccountToken := c.Token != ""
	hasProjectToken := c.ProjectToken != ""
	switch {
	case hasAccountToken && hasProjectToken:
		return errors.New("configure only one of railway:token or railway:projectToken")
	case !hasAccountToken && !hasProjectToken:
		return errors.New("configure railway:token or railway:projectToken")
	case hasProjectToken:
		c.client = NewProjectClient(c.ProjectToken)
	default:
		c.client = NewClient(c.Token)
	}
	return nil
}

type clientContextKey struct{}

func getClient(ctx context.Context) *Client {
	if client, ok := ctx.Value(clientContextKey{}).(*Client); ok {
		return client
	}
	cfg := infer.GetConfig[ProviderConfig](ctx)
	if cfg.client == nil {
		panic("Railway provider used before Configure initialized its API client")
	}
	return cfg.client
}

func contextWithClient(ctx context.Context, client *Client) context.Context {
	return context.WithValue(ctx, clientContextKey{}, client)
}

func BuildProvider() (provider.Provider, error) {
	return infer.NewProviderBuilder().
		WithConfig(infer.Config(ProviderConfig{})).
		WithResources(
			infer.Resource(&Project{}),
			infer.Resource(&Service{}),
			infer.Resource(&Variable{}),
			infer.Resource(&CustomDomainResource{}),
			infer.Resource(&Volume{}),
		).
		WithLanguageMap(map[string]any{
			"nodejs": map[string]any{
				"packageName":          "@thegreataxios/pulumi-railway",
				"packageDescription":   "Native Pulumi provider for Railway: projects, services, variables, custom domains, and volumes",
				"respectSchemaVersion": true,
				"typescriptVersion":    "^5.9.2",
				"dependencies":         map[string]string{"@pulumi/pulumi": "^3.142.0"},
			},
			"go": map[string]any{
				"generateResourceContainerTypes": true,
				"importBasePath":                 "github.com/thegreataxios/pulumi-railway/sdk/go/railway",
				"respectSchemaVersion":           true,
			},
			"python": map[string]any{
				"respectSchemaVersion": true,
				"pyproject":            map[string]any{"enabled": true},
			},
			"csharp": map[string]any{"respectSchemaVersion": true},
		}).
		WithDescription("A Pulumi native provider for managing Railway infrastructure.").
		WithDisplayName("Railway").
		WithKeywords("pulumi", "pulumi-provider", "railway", "infrastructure-as-code", "iac", "cloud").
		WithHomepage("https://github.com/thegreataxios/pulumi-railway").
		WithRepository("https://github.com/thegreataxios/pulumi-railway").
		WithPublisher("thegreataxios").
		WithNamespace("thegreataxios").
		WithLicense("MIT").
		WithPluginDownloadURL(pluginDownloadURL).
		Build()
}
