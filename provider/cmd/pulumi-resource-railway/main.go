package main

import (
	"context"
	"log"

	pkg "github.com/thegreataxios/pulumi-railway/provider/pkg"
	"github.com/thegreataxios/pulumi-railway/provider/pkg/version"
)

func main() {
	provider, err := pkg.BuildProvider()
	if err != nil {
		log.Fatalf("failed to build provider: %v", err)
	}

	if err := provider.Run(context.Background(), "railway", version.Version); err != nil {
		log.Fatalf("failed to run provider: %v", err)
	}
}
