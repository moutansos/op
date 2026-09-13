package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/server"
	"github.com/moutansos/op/internal/tui"
)

func TestDashboardHostsAuthenticatedStateAndStopsOnExit(t *testing.T) {
	for _, args := range [][]string{{"dashboard"}, {"--no-target"}} {
		t.Run(args[0], func(t *testing.T) {
			runtime := newTestRuntime()
			runtime.service.ensureResult.StartDashboard = true
			options := runtime.options()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			load := options.LoadConfig
			options.LoadConfig = func(path string) (config.LoadResult, error) {
				result, err := load(path)
				result.Config.Server.Enabled = true
				result.Config.Server.Listen = address
				result.Config.Server.Token = "config-token"
				return result, err
			}
			options.RunServer = runServer
			options.ReadFile = func(string) ([]byte, error) { return nil, errors.New("token file should not be read") }
			uiRan := false
			options.RunTUI = func(ctx context.Context, _ domain.Service, _ tui.Options) error {
				uiRan = true
				client := &http.Client{Timeout: time.Second}
				for _, token := range []string{"", "config-token"} {
					request, _ := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/v1/state", nil)
					if token != "" {
						request.Header.Set("Authorization", "Bearer "+token)
					}
					response, err := client.Do(request)
					if err != nil {
						return err
					}
					if token == "" {
						if response.StatusCode != 401 {
							t.Errorf("unauthenticated status = %d", response.StatusCode)
						}
					} else {
						var body struct {
							InstanceID string `json:"instanceId"`
						}
						if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if response.StatusCode != 200 || body.InstanceID != "test-instance" {
							t.Errorf("state: status %d, %+v", response.StatusCode, body)
						}
					}
					response.Body.Close()
				}
				client.CloseIdleConnections()
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if code := Run(ctx, args, options); code != 0 {
				t.Fatalf("exit %d: %s", code, runtime.stderr.String())
			}
			if !uiRan {
				t.Fatal("dashboard not started")
			}
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err == nil {
				conn.Close()
				t.Fatal("server survived dashboard exit")
			}
		})
	}
}

func TestDashboardOccupiedPortKeepsUIWithoutPublishing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	runtime := newTestRuntime()
	options := runtime.options()
	load := options.LoadConfig
	options.LoadConfig = func(path string) (config.LoadResult, error) {
		result, err := load(path)
		result.Config.Server.Enabled = true
		result.Config.Server.Listen = listener.Addr().String()
		return result, err
	}
	started := false
	options.RunServer = func(ctx context.Context, service domain.Service, opts server.Options) error {
		ready := opts.OnListening
		opts.OnListening = func() { started = true; ready() }
		return runServer(ctx, service, opts)
	}
	if code := Run(context.Background(), []string{"dashboard"}, options); code != 0 {
		t.Fatalf("exit %d: %s", code, runtime.stderr.String())
	}
	if runtime.tuiCalls != 1 || started {
		t.Fatalf("UI calls=%d, publisher started=%v", runtime.tuiCalls, started)
	}
}

func TestDashboardServerFailureCancelsUI(t *testing.T) {
	runtime := newTestRuntime()
	options := runtime.options()
	load := options.LoadConfig
	options.LoadConfig = func(path string) (config.LoadResult, error) {
		result, err := load(path)
		result.Config.Server.Enabled = true
		return result, err
	}
	uiStarted := make(chan struct{})
	options.RunServer = func(ctx context.Context, _ domain.Service, opts server.Options) error {
		opts.OnListening()
		<-uiStarted
		return errors.New("listener failed")
	}
	options.RunTUI = func(ctx context.Context, _ domain.Service, _ tui.Options) error {
		close(uiStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if code := Run(ctx, []string{"dashboard"}, options); code == 0 {
		t.Fatal("server error lost")
	}
}

func TestConfiguredAPITokenPrecedence(t *testing.T) {
	for _, env := range []string{"", "env-token"} {
		runtime := newTestRuntime()
		runtime.lookup["OP_API_TOKEN"] = env
		options := runtime.options()
		load := options.LoadConfig
		options.LoadConfig = func(path string) (config.LoadResult, error) {
			result, err := load(path)
			result.Config.Server.Token = " config-token \n"
			return result, err
		}
		options.ReadFile = func(string) ([]byte, error) { return nil, errors.New("token file should not be read") }
		if code := Run(context.Background(), []string{"serve"}, options); code != 0 {
			t.Fatalf("exit %d: %s", code, runtime.stderr.String())
		}
		want := env
		if want == "" {
			want = "config-token"
		}
		if runtime.serverOptions.Token != want {
			t.Fatalf("token=%q, want %q", runtime.serverOptions.Token, want)
		}
	}
}

func TestServerInvalidTLSDoesNotStartRuntime(t *testing.T) {
	started := false
	options := server.DefaultOptions()
	options.ListenAddress = "127.0.0.1:0"
	options.Token = "token"
	options.TLSCertFile = filepath.Join(t.TempDir(), "missing-cert")
	options.TLSKeyFile = filepath.Join(t.TempDir(), "missing-key")
	options.OnListening = func() { started = true }
	if err := runServer(context.Background(), &fakeService{}, options); err == nil {
		t.Fatal("invalid TLS accepted")
	}
	if started {
		t.Fatal("runtime started before TLS initialization succeeded")
	}
}
