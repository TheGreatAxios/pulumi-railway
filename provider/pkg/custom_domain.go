package pkg

import (
	"context"
	"fmt"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// CustomDomainResource manages a Railway custom domain.
type CustomDomainResource struct{}

func (domain *CustomDomainResource) Annotate(a infer.Annotator) {
	a.Describe(domain, "A custom domain attached to a Railway service instance.")
	a.SetToken("index", "CustomDomain")
	a.AddAlias("pkg", "CustomDomains")
	a.AddAlias("pkg", "CustomDomain")
}

type CustomDomainArgs struct {
	// Project ID.
	ProjectID string `pulumi:"projectId" provider:"replaceOnChanges"`
	// Environment ID.
	EnvironmentID string `pulumi:"environmentId" provider:"replaceOnChanges"`
	// Service ID.
	ServiceID string `pulumi:"serviceId" provider:"replaceOnChanges"`
	// Domain name, e.g. "api.example.com".
	Domain string `pulumi:"domain" provider:"replaceOnChanges"`
	// Target port for the domain.
	TargetPort *int `pulumi:"targetPort,optional"`
}

func (args *CustomDomainArgs) Annotate(a infer.Annotator) {
	a.Describe(args, "Inputs for managing a Railway custom domain.")
	a.Describe(&args.ProjectID, "ID of the Railway project that owns the domain.")
	a.Describe(&args.EnvironmentID, "ID of the Railway environment where the domain is attached.")
	a.Describe(&args.ServiceID, "ID of the Railway service where the domain is attached.")
	a.Describe(&args.Domain, "Hostname without a scheme, path, or port, for example api.example.com.")
	a.Describe(&args.TargetPort, "Optional container port to which Railway routes domain traffic.")
}

type CustomDomainState struct {
	CustomDomainArgs
	// Railway custom domain ID.
	RailwayID string `pulumi:"railwayId"`
	// Verification token for DNS TXT record.
	VerificationToken string `pulumi:"verificationToken,optional"`
	// DNS CNAME target (the Railway service domain).
	CNAMETarget string `pulumi:"cnameTarget,optional"`
	// Certificate status: PENDING | ISSUED | FAILED.
	CertificateStatus string `pulumi:"certificateStatus,optional"`
}

func (state *CustomDomainState) Annotate(a infer.Annotator) {
	a.Describe(state, "The observed state and DNS status of a Railway custom domain.")
	a.Describe(&state.ProjectID, "ID of the Railway project that owns the domain.")
	a.Describe(&state.EnvironmentID, "ID of the Railway environment where the domain is attached.")
	a.Describe(&state.ServiceID, "ID of the Railway service where the domain is attached.")
	a.Describe(&state.Domain, "Custom domain hostname.")
	a.Describe(&state.TargetPort, "Optional container port to which Railway routes domain traffic.")
	a.Describe(&state.RailwayID, "Railway custom domain ID.")
	a.Describe(&state.VerificationToken, "Token used to verify domain ownership.")
	a.Describe(&state.CNAMETarget, "DNS CNAME value required by Railway.")
	a.Describe(&state.CertificateStatus, "Current Railway TLS certificate status.")
}

func (*CustomDomainResource) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[CustomDomainArgs], error) {
	return checkInputs(ctx, req, func(inputs property.Map, input CustomDomainArgs) []provider.CheckFailure {
		failures := appendFailures(
			required(inputs, "projectId", input.ProjectID),
			required(inputs, "environmentId", input.EnvironmentID),
			required(inputs, "serviceId", input.ServiceID),
			required(inputs, "domain", input.Domain),
		)
		if !isUnknown(inputs, "domain") &&
			(strings.Contains(input.Domain, "://") || strings.ContainsAny(input.Domain, "/:")) {
			failures = append(failures, provider.CheckFailure{
				Property: "domain", Reason: "domain must be a hostname without a scheme, path, or port",
			})
		}
		if input.TargetPort != nil && !isUnknown(inputs, "targetPort") &&
			(*input.TargetPort < 1 || *input.TargetPort > 65535) {
			failures = append(failures, provider.CheckFailure{
				Property: "targetPort", Reason: "targetPort must be between 1 and 65535",
			})
		}
		return failures
	})
}

func (*CustomDomainResource) Create(
	ctx context.Context, req infer.CreateRequest[CustomDomainArgs],
) (infer.CreateResponse[CustomDomainState], error) {
	input := req.Inputs
	state := CustomDomainState{CustomDomainArgs: input}
	if req.DryRun {
		return infer.CreateResponse[CustomDomainState]{ID: req.Name, Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.CreateResponse[CustomDomainState]{}, err
	}

	var result struct {
		CustomDomainCreate struct {
			ID     string `json:"id"`
			Domain string `json:"domain"`
			Status struct {
				VerificationToken string `json:"verificationToken"`
				DNSRecords        []struct {
					Hostlabel     string `json:"hostlabel"`
					RequiredValue string `json:"requiredValue"`
				} `json:"dnsRecords"`
			} `json:"status"`
		} `json:"customDomainCreate"`
	}

	mutation := `mutation customDomainCreate($input: CustomDomainCreateInput!) {
  customDomainCreate(input: $input) {
    id domain
    status {
      verificationToken
      dnsRecords { hostlabel requiredValue }
    }
  }
}`

	createInput := map[string]interface{}{
		"projectId":     input.ProjectID,
		"environmentId": input.EnvironmentID,
		"serviceId":     input.ServiceID,
		"domain":        input.Domain,
	}
	if input.TargetPort != nil {
		createInput["targetPort"] = *input.TargetPort
	}

	if err := client.mutate(ctx, mutation, map[string]interface{}{"input": createInput}, &result); err != nil {
		return infer.CreateResponse[CustomDomainState]{}, fmt.Errorf("create custom domain: %w", err)
	}
	if err := requireCreatedID("custom domain", result.CustomDomainCreate.ID); err != nil {
		return infer.CreateResponse[CustomDomainState]{}, err
	}

	state.RailwayID = result.CustomDomainCreate.ID
	state.VerificationToken = result.CustomDomainCreate.Status.VerificationToken
	for _, rec := range result.CustomDomainCreate.Status.DNSRecords {
		if rec.Hostlabel == "" || rec.Hostlabel == "@" {
			state.CNAMETarget = rec.RequiredValue
			break
		}
	}

	return infer.CreateResponse[CustomDomainState]{ID: result.CustomDomainCreate.ID, Output: state}, nil
}

func (*CustomDomainResource) Read(
	ctx context.Context, req infer.ReadRequest[CustomDomainArgs, CustomDomainState],
) (infer.ReadResponse[CustomDomainArgs, CustomDomainState], error) {
	state := req.State
	client, err := getClient(ctx)
	if err != nil {
		return infer.ReadResponse[CustomDomainArgs, CustomDomainState]{}, err
	}
	domainID := req.ID
	inputs := req.Inputs
	if strings.Contains(req.ID, "/") {
		parts := strings.SplitN(req.ID, "/", 4)
		if len(parts) != 4 {
			return infer.ReadResponse[CustomDomainArgs, CustomDomainState]{}, fmt.Errorf(
				"invalid custom domain import ID %q; expected customDomainId/projectId/environmentId/serviceId",
				req.ID,
			)
		}
		domainID = parts[0]
		inputs.ProjectID = parts[1]
		inputs.EnvironmentID = parts[2]
		inputs.ServiceID = parts[3]
	}
	if strings.TrimSpace(inputs.ProjectID) == "" {
		return infer.ReadResponse[CustomDomainArgs, CustomDomainState]{}, fmt.Errorf(
			"custom domain import ID must be customDomainId/projectId/environmentId/serviceId",
		)
	}

	var result struct {
		CustomDomain struct {
			ID         string `json:"id"`
			Domain     string `json:"domain"`
			TargetPort *int   `json:"targetPort"`
			Status     struct {
				VerificationToken string `json:"verificationToken"`
				CertificateStatus string `json:"certificateStatus"`
				DNSRecords        []struct {
					Hostlabel     string `json:"hostlabel"`
					RequiredValue string `json:"requiredValue"`
				} `json:"dnsRecords"`
			} `json:"status"`
		} `json:"customDomain"`
	}

	query := `query customDomain($id: String!, $projectId: String!) {
  customDomain(id: $id, projectId: $projectId) {
    id domain targetPort
    status {
      verificationToken certificateStatus
      dnsRecords { hostlabel requiredValue }
    }
  }
}`

	if err := client.query(ctx, query, map[string]interface{}{
		"id":        domainID,
		"projectId": inputs.ProjectID,
	}, &result); err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[CustomDomainArgs, CustomDomainState]{}, nil
		}
		return infer.ReadResponse[CustomDomainArgs, CustomDomainState]{}, fmt.Errorf("read custom domain: %w", err)
	}
	if result.CustomDomain.ID == "" {
		return infer.ReadResponse[CustomDomainArgs, CustomDomainState]{}, nil
	}

	state.RailwayID = result.CustomDomain.ID
	state.Domain = result.CustomDomain.Domain
	state.ProjectID = inputs.ProjectID
	state.EnvironmentID = inputs.EnvironmentID
	state.ServiceID = inputs.ServiceID
	state.TargetPort = result.CustomDomain.TargetPort
	state.VerificationToken = result.CustomDomain.Status.VerificationToken
	state.CertificateStatus = result.CustomDomain.Status.CertificateStatus
	state.CNAMETarget = ""
	for _, record := range result.CustomDomain.Status.DNSRecords {
		if record.Hostlabel == "" || record.Hostlabel == "@" {
			state.CNAMETarget = record.RequiredValue
			break
		}
	}
	return infer.ReadResponse[CustomDomainArgs, CustomDomainState]{
		ID: domainID, Inputs: state.CustomDomainArgs, State: state,
	}, nil
}

func (*CustomDomainResource) Update(
	ctx context.Context, req infer.UpdateRequest[CustomDomainArgs, CustomDomainState],
) (infer.UpdateResponse[CustomDomainState], error) {
	input := req.Inputs
	state := req.State
	if req.DryRun {
		state.CustomDomainArgs = input
		return infer.UpdateResponse[CustomDomainState]{Output: state}, nil
	}

	client, err := getClient(ctx)
	if err != nil {
		return infer.UpdateResponse[CustomDomainState]{}, err
	}

	if !equalPointers(state.TargetPort, input.TargetPort) {
		mutation := `mutation customDomainUpdate($id: String!, $environmentId: String!, $targetPort: Int) {
  customDomainUpdate(id: $id, environmentId: $environmentId, targetPort: $targetPort)
}`
		var targetPort interface{}
		if input.TargetPort != nil {
			targetPort = *input.TargetPort
		}
		vars := map[string]interface{}{
			"id":            req.ID,
			"environmentId": input.EnvironmentID,
			"targetPort":    targetPort,
		}
		if err := client.mutate(ctx, mutation, vars, nil); err != nil {
			return infer.UpdateResponse[CustomDomainState]{}, fmt.Errorf("update custom domain: %w", err)
		}
	}

	state.CustomDomainArgs = input
	return infer.UpdateResponse[CustomDomainState]{Output: state}, nil
}

func (*CustomDomainResource) Delete(
	ctx context.Context, req infer.DeleteRequest[CustomDomainState],
) (infer.DeleteResponse, error) {
	client, err := getClient(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}

	mutation := `mutation customDomainDelete($id: String!) { customDomainDelete(id: $id) }`

	if err := client.mutate(ctx, mutation, map[string]interface{}{"id": req.ID}, nil); err != nil {
		if isNotFound(err) {
			return infer.DeleteResponse{}, nil
		}
		return infer.DeleteResponse{}, fmt.Errorf("delete custom domain: %w", err)
	}

	return infer.DeleteResponse{}, nil
}
