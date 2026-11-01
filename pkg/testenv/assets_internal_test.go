package testenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func stubRunner(t *testing.T, fn func(context.Context) (string, error)) {
	t.Helper()
	orig := runSetupEnvtest
	runSetupEnvtest = fn
	t.Cleanup(func() { runSetupEnvtest = orig })
}

func TestAssetsPrefersTheEnvironment(t *testing.T) {
	stubRunner(t, func(context.Context) (string, error) {
		t.Error("setup-envtest ran though KUBEBUILDER_ASSETS is set")
		return "", nil
	})
	t.Setenv(assetsEnv, "/opt/envtest")
	got, err := Assets(context.Background())
	if err != nil || got != "/opt/envtest" {
		t.Errorf("Assets = %q, %v", got, err)
	}
}

func TestAssetsFallsBackToSetupEnvtest(t *testing.T) {
	t.Setenv(assetsEnv, "")
	stubRunner(t, func(context.Context) (string, error) { return "/fetched", nil })
	if got, err := Assets(context.Background()); err != nil || got != "/fetched" {
		t.Errorf("Assets = %q, %v", got, err)
	}
	boom := errors.New("boom")
	stubRunner(t, func(context.Context) (string, error) { return "", boom })
	if _, err := Assets(context.Background()); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

// The real runner downloads the pinned kube-apiserver and etcd (cached after the
// first run) and prints their directory.
func TestRunSetupEnvtestFetchesThePinnedRelease(t *testing.T) {
	dir, err := runSetupEnvtest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"kube-apiserver", "etcd"} {
		if _, err := os.Stat(filepath.Join(dir, bin)); err != nil {
			t.Errorf("%s: %v", bin, err)
		}
	}
}

func TestRunSetupEnvtestReportsAMissingGoToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no go binary
	if _, err := runSetupEnvtest(context.Background()); err == nil {
		t.Error("expected an error")
	}
}

func TestStartFailsWithoutAssets(t *testing.T) {
	t.Setenv(assetsEnv, "")
	boom := errors.New("no assets")
	stubRunner(t, func(context.Context) (string, error) { return "", boom })
	if e, err := Start(context.Background(), Options{}); !errors.Is(err, boom) || e != nil {
		t.Errorf("Start = %v, %v", e, err)
	}
}

func TestStopShutsTheEnvironmentDown(t *testing.T) {
	e, err := Start(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(); err != nil {
		t.Error(err)
	}
}

func TestStartFailsWhenACRDPathIsMissing(t *testing.T) {
	e, err := Start(context.Background(), Options{CRDPaths: []string{filepath.Join(t.TempDir(), "nope")}})
	if err == nil || e != nil {
		t.Errorf("Start = %v, %v; want an error", e, err)
	}
}

func TestStartFailsWhenTheClientCannotBeBuilt(t *testing.T) {
	boom := errors.New("client boom")
	orig := newClient
	newClient = func(*rest.Config, client.Options) (client.Client, error) { return nil, boom }
	t.Cleanup(func() { newClient = orig })

	e, err := Start(context.Background(), Options{})
	if !errors.Is(err, boom) || e != nil {
		t.Errorf("Start = %v, %v; want the client error", e, err)
	}
}
