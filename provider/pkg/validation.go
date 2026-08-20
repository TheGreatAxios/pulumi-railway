package pkg

import (
	"context"
	"fmt"
	"strings"

	provider "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

func checkInputs[T any](
	ctx context.Context,
	req infer.CheckRequest,
	validate func(property.Map, T) []provider.CheckFailure,
) (infer.CheckResponse[T], error) {
	inputs, failures, err := infer.DefaultCheck[T](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[T]{Inputs: inputs, Failures: failures}, err
	}
	return infer.CheckResponse[T]{Inputs: inputs, Failures: validate(req.NewInputs, inputs)}, nil
}

// isUnknown reports whether a property's value is not known yet, for example
// when it is the output of another resource during a preview. Unknown values
// decode as zero values, so validation must skip them.
func isUnknown(inputs property.Map, name string) bool {
	value, ok := inputs.GetOk(name)
	return ok && value.HasComputed()
}

func required(inputs property.Map, name, value string) []provider.CheckFailure {
	if isUnknown(inputs, name) || strings.TrimSpace(value) != "" {
		return nil
	}
	return []provider.CheckFailure{{Property: name, Reason: name + " must not be empty"}}
}

func appendFailures(groups ...[]provider.CheckFailure) []provider.CheckFailure {
	var failures []provider.CheckFailure
	for _, group := range groups {
		failures = append(failures, group...)
	}
	return failures
}

func requireCreatedID(resource, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("railway returned an empty ID after creating %s", resource)
	}
	return nil
}
