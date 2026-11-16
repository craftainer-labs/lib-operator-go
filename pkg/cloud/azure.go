package cloud

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	azcloud "github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// Entra token scopes of the data planes (ADR 0002, section 16.4).
const (
	// AzurePostgresScope is the scope of an Azure Database for PostgreSQL
	// flexible server login; the token is the password.
	AzurePostgresScope = "https://ossrdbms-aad.database.windows.net/.default"
	// AzureRedisScope is the scope of an Azure Managed Redis login.
	AzureRedisScope = "https://redis.azure.com/.default"
)

// AzureClient is what Egress.Azure resolves.
type AzureClient struct {
	// Credential gets Entra tokens; pass it to a service client.
	Credential azcore.TokenCredential
	// ClientOptions go to every service client (arm.ClientOptions embeds
	// them): the transport dials through the Policy, and the cloud's Entra
	// authority host is the override when one is set.
	ClientOptions azcore.ClientOptions
	// Endpoint is the endpointOverride, "" for none; the operator points its
	// own service client at it.
	Endpoint string
}

// Azure returns the credential of a provider.azure block. WorkloadIdentity is
// azidentity's WorkloadIdentityCredential, from the variables the Azure workload
// identity webhook injects (AZURE_CLIENT_ID, AZURE_FEDERATED_TOKEN_FILE) and
// the block's tenant; Secret is a client secret from secret["clientId"] and
// secret["clientSecret"]. Managed identity is not used: its endpoint is
// link-local and the Policy blocks it. The authority, and every call the
// credential makes, goes through the Policy. The authority must be https
// (ErrInvalidEndpoint otherwise), and the override is that authority; an
// override disables Entra instance discovery, as a private authority needs.
// Tokens are fetched on first use.
func (e Egress) Azure(_ context.Context, scope Scope, p AzureClusterBlock, c *Credentials, secret map[string][]byte) (*AzureClient, error) {
	endpoint, err := e.endpoint(scope, p.EndpointOverride)
	if err != nil {
		return nil, err
	}
	if endpoint != "" && !strings.HasPrefix(strings.ToLower(endpoint), "https://") {
		return nil, fmt.Errorf("%w: the Entra authority must be https", ErrInvalidEndpoint)
	}
	src, err := source(scope, c)
	if err != nil {
		return nil, err
	}
	hc, err := e.HTTPClient()
	if err != nil {
		return nil, err
	}
	// An explicit cloud also keeps AZURE_AUTHORITY_HOST from choosing the
	// authority; sovereign clouds are out of v1alpha1 (ADR 0002, 16.8).
	co := azcore.ClientOptions{Transport: hc, Cloud: azcloud.AzurePublic}
	if endpoint != "" {
		co.Cloud = azcloud.Configuration{ActiveDirectoryAuthorityHost: endpoint}
	}
	var cred azcore.TokenCredential
	if src == SourceSecret {
		cred, err = azureSecret(p.TenantID, secret, co, endpoint != "")
	} else {
		cred, err = azidentity.NewWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{
			ClientOptions:            co,
			TenantID:                 p.TenantID,
			DisableInstanceDiscovery: endpoint != "",
		})
		if err != nil {
			err = fmt.Errorf("%w: no Azure workload identity is injected", ErrCredentialsUnavailable)
		}
	}
	if err != nil {
		return nil, err
	}
	return &AzureClient{Credential: cred, ClientOptions: co, Endpoint: endpoint}, nil
}

func azureSecret(tenant string, secret map[string][]byte, co azcore.ClientOptions, private bool) (azcore.TokenCredential, error) {
	get := func(k string) string { return strings.TrimSpace(string(secret[k])) }
	id, key := get(KeyAzureClientID), get(KeyAzureClientSecret)
	if id == "" || key == "" {
		return nil, fmt.Errorf("%w: the Secret needs the keys %s and %s", ErrCredentialsUnavailable, KeyAzureClientID, KeyAzureClientSecret)
	}
	cred, err := azidentity.NewClientSecretCredential(tenant, id, key, &azidentity.ClientSecretCredentialOptions{
		ClientOptions:            co,
		DisableInstanceDiscovery: private,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: the Azure client secret credential is invalid", ErrCredentialsUnavailable)
	}
	return cred, nil
}

// AzureTokenSource returns the TokenSource of an Entra token for scope
// (AzurePostgresScope, AzureRedisScope). The credential caches tokens
// itself; a token is requested just before a connect.
func AzureTokenSource(cred azcore.TokenCredential, scope string) TokenSource {
	return TokenFunc(func(ctx context.Context) (string, time.Time, error) {
		tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
		if err != nil {
			return "", time.Time{}, fmt.Errorf("%w: the Entra token could not be fetched", ErrCredentialsUnavailable)
		}
		return tok.Token, tok.ExpiresOn, nil
	})
}
