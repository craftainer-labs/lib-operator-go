package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/craftainer-labs/lib-operator-go/pkg/egress"
)

// isolateAWS hides the machine's AWS configuration from the SDK's chain.
func isolateAWS(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_ROLE_ARN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_RETRY_MODE", "AWS_ENDPOINT_URL",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", writeFile(t, "config", ""))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", writeFile(t, "credentials", ""))
}

// fakeSTS answers AssumeRole and AssumeRoleWithWebIdentity with fixed credentials.
func fakeSTS(t *testing.T) (url string, h *hits) {
	t.Helper()
	srv, h := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		action := "AssumeRole"
		if r.Form.Get("Action") == "AssumeRoleWithWebIdentity" {
			action = "AssumeRoleWithWebIdentity"
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = fmt.Fprintf(w, `<%[1]sResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><%[1]sResult>`+
			`<Credentials><AccessKeyId>ASIAFAKE</AccessKeyId><SecretAccessKey>fake-secret-value</SecretAccessKey>`+
			`<SessionToken>fake-session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>`+
			`<AssumedRoleUser><AssumedRoleId>AROAFAKE:s</AssumedRoleId><Arn>arn:aws:sts::111122223333:assumed-role/r/s</Arn></AssumedRoleUser>`+
			`</%[1]sResult><ResponseMetadata><RequestId>req</RequestId></ResponseMetadata></%[1]sResponse>`, action)
	})
	return srv.URL, h
}

var awsSecret = &Credentials{Source: SourceSecret, SecretRef: &SecretRef{Name: "aws"}}

func awsKeys() map[string][]byte {
	return map[string][]byte{KeyAWSAccessKeyID: []byte("AKIAFAKEKEY\n"), KeyAWSSecretAccessKey: []byte("fake-secret-key"), KeyAWSSessionToken: []byte("fake-token")}
}

func TestAWSSecretStaticKeys(t *testing.T) {
	isolateAWS(t)
	cfg, err := openEgress().AWS(t.Context(), ScopeNamespaced, AWSClusterBlock{AWSBlock: AWSBlock{Region: "eu-west-1"}}, awsSecret, awsKeys())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "eu-west-1" || cfg.HTTPClient == nil {
		t.Errorf("config = region %q, client %v", cfg.Region, cfg.HTTPClient)
	}
	got, err := cfg.Credentials.Retrieve(t.Context())
	if err != nil || got.AccessKeyID != "AKIAFAKEKEY" || got.SecretAccessKey != "fake-secret-key" || got.SessionToken != "fake-token" {
		t.Errorf("credentials = %+v, %v", got, err)
	}
}

func TestAWSSecretWithAssumeRole(t *testing.T) {
	isolateAWS(t)
	sts, h := fakeSTS(t)
	p := AWSClusterBlock{AWSBlock: AWSBlock{Region: "eu-west-1", RoleARN: "arn:aws:iam::111122223333:role/r"}, EndpointOverride: sts}
	cfg, err := openEgress().AWS(t.Context(), ScopeCluster, p, awsSecret, awsKeys())
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.Credentials.Retrieve(t.Context())
	if err != nil || got.AccessKeyID != "ASIAFAKE" || got.SessionToken != "fake-session" {
		t.Errorf("credentials = %+v, %v", got, err)
	}
	if h.count() != 1 || h.field("Action") != "AssumeRole" || h.field("RoleArn") != p.RoleARN {
		t.Errorf("STS saw %d requests, Action %q, RoleArn %q", h.count(), h.field("Action"), h.field("RoleArn"))
	}
}

func TestAWSWorkloadIdentity(t *testing.T) {
	isolateAWS(t)
	sts, h := fakeSTS(t)
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", writeFile(t, "token", "projected-service-account-token"))
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::111122223333:role/irsa")
	p := AWSClusterBlock{AWSBlock: AWSBlock{Region: "eu-west-1"}, EndpointOverride: sts}
	for _, c := range []*Credentials{nil, {Source: SourceWorkloadIdentity}} {
		cfg, err := openEgress().AWS(t.Context(), ScopeCluster, p, c, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := cfg.Credentials.Retrieve(t.Context()); err != nil || got.AccessKeyID != "ASIAFAKE" {
			t.Errorf("credentials = %+v, %v", got, err)
		}
	}
	if h.field("Action") != "AssumeRoleWithWebIdentity" || h.field("RoleArn") != "arn:aws:iam::111122223333:role/irsa" {
		t.Errorf("STS Action %q, RoleArn %q", h.field("Action"), h.field("RoleArn"))
	}
}

func TestAWSNoWorkloadIdentity(t *testing.T) {
	isolateAWS(t)
	_, err := openEgress().AWS(t.Context(), ScopeCluster, AWSClusterBlock{AWSBlock: AWSBlock{Region: "eu-west-1"}}, nil, nil)
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("AWS = %v", err)
	}
}

func TestAWSSecretErrorsCarryNoSecret(t *testing.T) {
	isolateAWS(t)
	for name, secret := range map[string]map[string][]byte{
		"nil":            nil,
		"no secret key":  {KeyAWSAccessKeyID: []byte("AKIA-LEAK-CHECK")},
		"no access key":  {KeyAWSSecretAccessKey: []byte("secret-LEAK-CHECK")},
		"blank":          {KeyAWSAccessKeyID: []byte(" \n"), KeyAWSSecretAccessKey: []byte("secret-LEAK-CHECK")},
		"wrong key name": {"accessKey": []byte("AKIA-LEAK-CHECK"), "secretKey": []byte("secret-LEAK-CHECK")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := openEgress().AWS(t.Context(), ScopeCluster, AWSClusterBlock{}, awsSecret, secret)
			if !errors.Is(err, ErrCredentialsUnavailable) {
				t.Fatalf("AWS = %v", err)
			}
			noSecret(t, err, "AKIA-LEAK-CHECK", "secret-LEAK-CHECK")
		})
	}
}

func TestAWSInvalidEnvironment(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_RETRY_MODE", "bogus")
	_, err := openEgress().AWS(t.Context(), ScopeCluster, AWSClusterBlock{}, awsSecret, awsKeys())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("AWS = %v", err)
	}
	noSecret(t, err, "bogus", "fake-secret-key")
}

// The STS call of an AssumeRole goes through the Policy: the emulator on
// loopback is refused, and never sees a request.
func TestAWSTokenEndpointDialsThroughThePolicy(t *testing.T) {
	isolateAWS(t)
	sts, h := fakeSTS(t)
	p := AWSClusterBlock{AWSBlock: AWSBlock{Region: "eu-west-1", RoleARN: "arn:aws:iam::111122223333:role/r"}, EndpointOverride: sts}
	_, err := NewEgress(egress.ModeAllowPrivate, nil).AWS(t.Context(), ScopeCluster, p, awsSecret, awsKeys())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Fatalf("AWS = %v", err)
	}
	noSecret(t, err, "fake-secret-key", "fake-token", sts)
	if h.count() != 0 {
		t.Errorf("the STS fake was reached %d times", h.count())
	}
}

func TestRDSTokenSource(t *testing.T) {
	cfg := aws.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("AKIAFAKEKEY", "fake-secret-key", "")}
	before := time.Now()
	tok, exp, err := RDSTokenSource(cfg, "db.example.test:5432", "app_role").Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"db.example.test:5432?", "Action=connect", "DBUser=app_role", "X-Amz-Signature=", "X-Amz-Expires=900", "X-Amz-Credential=AKIAFAKEKEY"} {
		if !strings.Contains(tok, want) {
			t.Errorf("token lacks %q: %s", want, tok)
		}
	}
	if strings.Contains(tok, "fake-secret-key") {
		t.Error("the token holds the secret key")
	}
	if exp.Before(before.Add(RDSTokenLifetime)) || exp.After(time.Now().Add(RDSTokenLifetime)) {
		t.Errorf("expiry %v is not 15 minutes from issue", exp)
	}

	// The token cannot outlive the credentials that signed it.
	soon := time.Now().Add(2 * time.Minute)
	short := aws.Config{Region: "eu-west-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "ASIAFAKE", SecretAccessKey: "fake-secret-key", CanExpire: true, Expires: soon}, nil
	})}
	if _, exp, err := RDSTokenSource(short, "db.example.test:5432", "app_role").Token(t.Context()); err != nil || !exp.Equal(soon) {
		t.Errorf("expiry = %v, %v; want %v", exp, err, soon)
	}
	expired := aws.Config{Region: "eu-west-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "ASIAFAKE", SecretAccessKey: "fake-secret-key", CanExpire: true, Expires: time.Now().Add(-time.Minute)}, nil
	})}
	if _, _, err := RDSTokenSource(expired, "db.example.test:5432", "app_role").Token(t.Context()); !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("expired credentials: %v", err)
	}
	failing := aws.Config{Region: "eu-west-1", Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, errors.New("LEAK-CHECK")
	})}
	_, _, err = RDSTokenSource(failing, "db.example.test:5432", "app_role").Token(t.Context())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("failing credentials: %v", err)
	}
	noSecret(t, err, "LEAK-CHECK")

	// A missing port and missing credentials fail with a category.
	_, _, err = RDSTokenSource(cfg, "db.example.test", "app_role").Token(t.Context())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("no port: %v", err)
	}
	_, _, err = RDSTokenSource(aws.Config{Region: "eu-west-1"}, "db.example.test:5432", "app_role").Token(t.Context())
	if !errors.Is(err, ErrCredentialsUnavailable) {
		t.Errorf("no credentials: %v", err)
	}
}
