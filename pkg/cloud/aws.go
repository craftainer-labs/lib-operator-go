package cloud

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	rdsauth "github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// retrieveTimeout bounds the one credential retrieval AWS makes.
const retrieveTimeout = 30 * time.Second

// RDSTokenLifetime is how long an RDS IAM authentication token is valid.
const RDSTokenLifetime = 15 * time.Minute

// AWS returns the aws.Config of a provider.aws block: its region, an HTTP
// client that dials through the Policy (STS included), the endpoint override
// when allowed, and credentials from the source. WorkloadIdentity is the SDK's
// default chain (IRSA, EKS Pod Identity); Secret is the static keys of secret
// (accessKeyId, secretAccessKey, optional sessionToken). A RoleARN is assumed
// on top of either. The credentials are retrieved once, so a missing identity
// is ErrCredentialsUnavailable here and not on the first API call.
//
// EC2 instance metadata is a link-local address the Policy blocks, so a pod
// that relies on the node's role fails closed; use IRSA or Pod Identity.
func (e Egress) AWS(ctx context.Context, scope Scope, p AWSClusterBlock, c *Credentials, secret map[string][]byte) (aws.Config, error) {
	endpoint, err := e.endpoint(scope, p.EndpointOverride)
	if err != nil {
		return aws.Config{}, err
	}
	src, err := source(scope, c)
	if err != nil {
		return aws.Config{}, err
	}
	hc, err := e.HTTPClient()
	if err != nil {
		return aws.Config{}, err
	}
	opts := []func(*config.LoadOptions) error{config.WithRegion(p.Region), config.WithHTTPClient(hc)}
	if endpoint != "" {
		opts = append(opts, config.WithBaseEndpoint(endpoint),
			// The SDK builds the web identity STS client before it resolves the
			// base endpoint, so the override reaches it only through here.
			config.WithWebIdentityRoleCredentialOptions(func(o *stscreds.WebIdentityRoleOptions) {
				o.Client = sts.New(sts.Options{Region: p.Region, HTTPClient: hc, BaseEndpoint: aws.String(endpoint)})
			}))
	}
	if src == SourceSecret {
		static, err := awsStatic(secret)
		if err != nil {
			return aws.Config{}, err
		}
		opts = append(opts, config.WithCredentialsProvider(static))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("%w: the AWS configuration could not be loaded", ErrCredentialsUnavailable)
	}
	if p.RoleARN != "" {
		cfg.Credentials = aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(sts.NewFromConfig(cfg), p.RoleARN))
	}
	// The default chain probes EC2 instance metadata, which the Policy blocks and
	// the SDK retries: bound the wait.
	rctx, cancel := context.WithTimeout(ctx, retrieveTimeout)
	defer cancel()
	if _, err := cfg.Credentials.Retrieve(rctx); err != nil {
		return aws.Config{}, fmt.Errorf("%w: AWS credentials could not be retrieved", ErrCredentialsUnavailable)
	}
	return cfg, nil
}

// awsStatic reads the static keys of a Secret. A trailing newline, which a
// Secret made from a file carries, is trimmed.
func awsStatic(secret map[string][]byte) (aws.CredentialsProvider, error) {
	get := func(k string) string { return strings.TrimSpace(string(secret[k])) }
	id, key := get(KeyAWSAccessKeyID), get(KeyAWSSecretAccessKey)
	if id == "" || key == "" {
		return nil, fmt.Errorf("%w: the Secret needs the keys %s and %s", ErrCredentialsUnavailable, KeyAWSAccessKeyID, KeyAWSSecretAccessKey)
	}
	return credentials.NewStaticCredentialsProvider(id, key, get(KeyAWSSessionToken)), nil
}

// RDSTokenSource returns the TokenSource of an RDS or Aurora IAM login:
// endpoint is the database's host:port and user the database role. A token is
// built locally, by signing, for RDSTokenLifetime. It is meant to be built per
// connection, so wrap it in a CachedSource only where a token is reused.
func RDSTokenSource(cfg aws.Config, endpoint, user string) TokenSource {
	return TokenFunc(func(ctx context.Context) (string, time.Time, error) {
		issued := time.Now()
		if cfg.Credentials == nil {
			return "", time.Time{}, fmt.Errorf("%w: no AWS credentials", ErrCredentialsUnavailable)
		}
		creds, err := cfg.Credentials.Retrieve(ctx)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("%w: AWS credentials could not be retrieved", ErrCredentialsUnavailable)
		}
		tok, err := rdsauth.BuildAuthToken(ctx, endpoint, cfg.Region, user,
			aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return creds, nil }))
		if err != nil {
			return "", time.Time{}, fmt.Errorf("%w: the RDS IAM token could not be built", ErrCredentialsUnavailable)
		}
		// The token was signed with creds, the ones fetched above, and stops
		// validating when they expire.
		exp := issued.Add(RDSTokenLifetime)
		if creds.CanExpire && creds.Expires.Before(exp) {
			exp = creds.Expires
		}
		if !exp.After(issued) {
			return "", time.Time{}, fmt.Errorf("%w: the AWS credentials have expired", ErrCredentialsUnavailable)
		}
		return tok, exp, nil
	})
}
