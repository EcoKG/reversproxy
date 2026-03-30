package app

import (
	"context"
	"testing"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
)

func TestServerApp_New(t *testing.T) {
	cfg := config.DefaultServerConfig()

	app, err := NewServerApp(cfg)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}

	if app == nil {
		t.Fatal("NewServerApp returned nil app")
	}

	if app.config != cfg {
		t.Error("ServerApp should store the provided config")
	}
}

func TestServerApp_Start_Stop(t *testing.T) {
	cfg := config.DefaultServerConfig()
	// Use random ports to avoid conflicts
	cfg.DataAddr = ":0"
	cfg.HTTPAddr = ":0"
	cfg.HTTPSAddr = ":0"
	cfg.AdminAddr = ":0"

	app, err := NewServerApp(cfg)
	if err != nil {
		t.Fatalf("NewServerApp failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start the app
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Start(ctx)
	}()

	// Give it time to start
	time.Sleep(100 * time.Millisecond)

	// Stop the app
	cancel()

	// Wait for shutdown
	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Errorf("App.Start returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("App did not shut down within timeout")
	}
}

func TestServerApp_ConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  *config.ServerConfig
		wantErr bool
	}{
		{
			name:    "valid config",
			config:  config.DefaultServerConfig(),
			wantErr: false,
		},
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "invalid data addr",
			config: &config.ServerConfig{
				DataAddr: "invalid:addr:format",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewServerApp(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewServerApp() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}