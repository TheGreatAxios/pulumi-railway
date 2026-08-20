package pkg

import (
	"context"
	"errors"
	"strings"
	"sync"

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
}

func (c *ProviderConfig) Annotate(a infer.Annotator) {
	a.Describe(c, "Configuration for authenticating with the Railway API.")
	a.Describe(&c.Token, "Railway account or workspace API token.")
	a.Describe(&c.ProjectToken, "Railway project token.")
	a.SetDefault(&c.Token, nil, "RAILWAY_API_TOKEN")
	a.SetDefault(&c.ProjectToken, nil, "RAILWAY_TOKEN")
}

// Configure validates credentials and pre-warms the shared API client. It
// must keep a value receiver: pulumi-go-provider asserts the config VALUE
// against CustomConfigure, so a pointer receiver silently never runs.
func (c ProviderConfig) Configure(context.Context) error {
	_, err := c.apiClient()
	return err
}

type authKey struct {
	token        string
	projectToken string
}

func (c ProviderConfig) authKey() authKey {
	return authKey{
		token:        strings.TrimSpace(c.Token),
		projectToken: strings.TrimSpace(c.ProjectToken),
	}
}

// clientCache memoizes one API client per credential so every RPC shares a
// single HTTP client (connection pooling, retry behavior) instead of building
// one per call. pulumi-go-provider hands out config copies, so the client
// cannot live on ProviderConfig itself.
var clientCache sync.Map // map[authKey]*Client

func (c ProviderConfig) newClient() (*Client, error) {
	key := c.authKey()
	switch {
	case key.token != "" && key.projectToken != "":
		return nil, errors.New("configure only one of railway:token or railway:projectToken")
	case key.token == "" && key.projectToken == "":
		return nil, errors.New("configure railway:token or railway:projectToken")
	case key.projectToken != "":
		return NewProjectClient(key.projectToken), nil
	default:
		return NewClient(key.token), nil
	}
}

func (c ProviderConfig) apiClient() (*Client, error) {
	key := c.authKey()
	if cached, ok := clientCache.Load(key); ok {
		return cached.(*Client), nil
	}
	client, err := c.newClient()
	if err != nil {
		return nil, err
	}
	cached, _ := clientCache.LoadOrStore(key, client)
	return cached.(*Client), nil
}

type clientContextKey struct{}

// getClient resolves the shared API client: an explicitly injected client in
// tests, otherwise the memoized client for the hydrated provider config.
func getClient(ctx context.Context) (*Client, error) {
	if client, ok := ctx.Value(clientContextKey{}).(*Client); ok {
		return client, nil
	}
	return infer.GetConfig[ProviderConfig](ctx).apiClient()
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
