// Package testenv is test support for operators built on this library: an
// envtest environment with the operator's CRDs installed, a namespace fixture,
// and an Events recorder with assertions. It needs the kube-apiserver and etcd
// binaries envtest runs; see Assets.
package testenv

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The setup-envtest release and Kubernetes version Assets fetches when
// KUBEBUILDER_ASSETS is not set. SetupEnvtestVersion tracks the
// controller-runtime release in go.mod; KubernetesVersion tracks k8s.io/api.
const (
	SetupEnvtestVersion = "v0.25.2"
	KubernetesVersion   = "1.37.0"
	assetsEnv           = "KUBEBUILDER_ASSETS"
)

// runSetupEnvtest returns the directory of the envtest binaries. It is a
// variable so a test can replace it.
var runSetupEnvtest = func(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "run",
		"sigs.k8s.io/controller-runtime/tools/setup-envtest@"+SetupEnvtestVersion,
		"use", KubernetesVersion, "-p", "path")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("setup-envtest: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// newClient is a variable so a test can make it fail.
var newClient = client.New

// Assets returns the directory of the kube-apiserver and etcd binaries:
// KUBEBUILDER_ASSETS when set, otherwise the pinned release fetched (and
// checksum-verified) by setup-envtest.
func Assets(ctx context.Context) (string, error) {
	if dir := os.Getenv(assetsEnv); dir != "" {
		return dir, nil
	}
	return runSetupEnvtest(ctx)
}

// Options configure Start.
type Options struct {
	// Scheme is the scheme of the Client; nil means client-go's scheme (core
	// types) only. Add the operator's API types to it.
	Scheme *runtime.Scheme
	// CRDs are installed before Start returns, and CRDPaths are directories or
	// files of CRD manifests.
	CRDs     []*apiextensionsv1.CustomResourceDefinition
	CRDPaths []string
	// Timeout bounds the API server start and the CRD installation; the default
	// is two minutes.
	Timeout time.Duration
}

// Env is a running envtest environment.
type Env struct {
	// Config reaches the API server; Client is a direct (uncached) client.
	Config *rest.Config
	Client client.Client
	Scheme *runtime.Scheme

	env *envtest.Environment
}

// Start runs an API server and etcd with the CRDs installed and returns the
// environment. Stop it with Stop.
func Start(ctx context.Context, opts Options) (*Env, error) {
	dir, err := Assets(ctx)
	if err != nil {
		return nil, err
	}
	scheme := opts.Scheme
	if scheme == nil {
		scheme = runtime.NewScheme()
		utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	env := &envtest.Environment{
		BinaryAssetsDirectory:    dir,
		CRDs:                     opts.CRDs,
		CRDDirectoryPaths:        opts.CRDPaths,
		ErrorIfCRDPathMissing:    true,
		ControlPlaneStartTimeout: timeout,
		CRDInstallOptions:        envtest.CRDInstallOptions{MaxTime: timeout},
	}
	cfg, err := env.Start()
	if err != nil {
		_ = env.Stop() // Start leaves the control plane running when a CRD fails to install
		return nil, fmt.Errorf("start envtest: %w", err)
	}
	c, err := newClient(cfg, client.Options{Scheme: scheme})
	if err != nil {
		_ = env.Stop()
		return nil, fmt.Errorf("envtest client: %w", err)
	}
	return &Env{Config: cfg, Client: c, Scheme: scheme, env: env}, nil
}

// Stop shuts the API server and etcd down.
func (e *Env) Stop() error { return e.env.Stop() }

// Namespace creates a namespace with a unique name and the given labels, and
// deletes it when the test ends.
func (e *Env) Namespace(t testing.TB, labels map[string]string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "t-", Labels: labels}}
	if err := e.Client.Create(context.Background(), ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		if err := e.Client.Delete(context.Background(), ns); err != nil {
			t.Errorf("delete namespace %s: %v", ns.Name, err)
		}
	})
	return ns.Name
}
