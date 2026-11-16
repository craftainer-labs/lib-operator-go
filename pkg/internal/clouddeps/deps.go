//go:build tools

// Package clouddeps pins the cloud SDK modules in go.mod before pkg/cloud
// (NK0) imports them: `go mod tidy` drops a requirement nothing imports, and
// the dependency-only change NK0a must keep them and their go.sum entries.
// The tools build tag keeps this file out of every build, test and the
// coverage gate; tidy still reads it. Delete this package in NK0 once
// pkg/cloud imports the same packages.
package clouddeps

import (
	_ "cloud.google.com/go/auth/credentials"
	_ "github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	_ "github.com/aws/aws-sdk-go-v2/config"
	_ "github.com/aws/aws-sdk-go-v2/credentials"
)
