package errors

import (
	"testing"

	"github.com/yassharma/gardener-simulator/pkg/types"
)

func TestNewInjectorWithConnectionErrors(t *testing.T) {
	cfg := types.ErrorInjectionConfig{
		Enabled:               true,
		ConnectionRefusedRate: 0.1,
		ConnectionResetRate:   0.2,
		IOTimeoutRate:         0.3,
		TokenExpiredErrorRate: 0.5,
	}

	inj := NewInjector(cfg)

	if inj.connectionRefusedRate != 0.1 {
		t.Errorf("expected connectionRefusedRate 0.1, got %v", inj.connectionRefusedRate)
	}
	if inj.connectionResetRate != 0.2 {
		t.Errorf("expected connectionResetRate 0.2, got %v", inj.connectionResetRate)
	}
	if inj.ioTimeoutRate != 0.3 {
		t.Errorf("expected ioTimeoutRate 0.3, got %v", inj.ioTimeoutRate)
	}
	if inj.tokenExpiredErrorRate != 0.5 {
		t.Errorf("expected tokenExpiredErrorRate 0.5, got %v", inj.tokenExpiredErrorRate)
	}
}

func TestSetServiceAccountKubeconfigBehavior(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{Enabled: true})

	// Set behavior
	inj.SetServiceAccountKubeconfigBehavior("garden-test", "sa-1", "token_expired_error")

	// Get behavior
	behavior := inj.GetServiceAccountKubeconfigBehavior("garden-test", "sa-1")
	if behavior != "token_expired_error" {
		t.Errorf("expected behavior 'token_expired_error', got '%s'", behavior)
	}

	// Test expired behavior
	inj.SetServiceAccountKubeconfigBehavior("garden-test", "sa-2", "expired")
	behavior = inj.GetServiceAccountKubeconfigBehavior("garden-test", "sa-2")
	if behavior != "expired" {
		t.Errorf("expected behavior 'expired', got '%s'", behavior)
	}

	// Test invalid behavior
	inj.SetServiceAccountKubeconfigBehavior("garden-test", "sa-3", "invalid")
	behavior = inj.GetServiceAccountKubeconfigBehavior("garden-test", "sa-3")
	if behavior != "invalid" {
		t.Errorf("expected behavior 'invalid', got '%s'", behavior)
	}

	// Clear behavior
	inj.SetServiceAccountKubeconfigBehavior("garden-test", "sa-1", "")
	behavior = inj.GetServiceAccountKubeconfigBehavior("garden-test", "sa-1")
	if behavior != "" {
		t.Errorf("expected behavior '', got '%s'", behavior)
	}
}

func TestClearServiceAccountKubeconfigBehaviors(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{Enabled: true})

	inj.SetServiceAccountKubeconfigBehavior("ns1", "sa1", "expired")
	inj.SetServiceAccountKubeconfigBehavior("ns2", "sa2", "invalid")

	inj.ClearServiceAccountKubeconfigBehaviors()

	if inj.GetServiceAccountKubeconfigBehavior("ns1", "sa1") != "" {
		t.Error("expected cleared behavior for ns1/sa1")
	}
	if inj.GetServiceAccountKubeconfigBehavior("ns2", "sa2") != "" {
		t.Error("expected cleared behavior for ns2/sa2")
	}
}

func TestSetConnectionErrorRates(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{Enabled: true})

	inj.SetConnectionErrorRates(0.25, 0.35, 0.15)

	if inj.connectionRefusedRate != 0.25 {
		t.Errorf("expected connectionRefusedRate 0.25, got %v", inj.connectionRefusedRate)
	}
	if inj.connectionResetRate != 0.35 {
		t.Errorf("expected connectionResetRate 0.35, got %v", inj.connectionResetRate)
	}
	if inj.ioTimeoutRate != 0.15 {
		t.Errorf("expected ioTimeoutRate 0.15, got %v", inj.ioTimeoutRate)
	}
}

func TestShouldInjectConnectionError_Disabled(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{
		Enabled:               false,
		ConnectionRefusedRate: 1.0, // 100% rate, but disabled
	})

	inject, _ := inj.ShouldInjectConnectionError()
	if inject {
		t.Error("expected no injection when disabled")
	}
}

func TestShouldInjectConnectionError_100Percent(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{
		Enabled:               true,
		ConnectionRefusedRate: 1.0, // 100% rate
	})

	// With 100% rate, should always inject
	for i := 0; i < 10; i++ {
		inject, errType := inj.ShouldInjectConnectionError()
		if !inject {
			t.Error("expected injection with 100% rate")
		}
		if errType != "connection refused" {
			t.Errorf("expected 'connection refused', got '%s'", errType)
		}
	}
}

func TestSetTokenExpiredErrorRate(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{Enabled: true})

	inj.SetTokenExpiredErrorRate(0.75)

	if inj.tokenExpiredErrorRate != 0.75 {
		t.Errorf("expected tokenExpiredErrorRate 0.75, got %v", inj.tokenExpiredErrorRate)
	}
}

func TestShouldInjectTokenExpiredError_Disabled(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{
		Enabled:               false,
		TokenExpiredErrorRate: 1.0, // 100% rate, but disabled
	})

	if inj.ShouldInjectTokenExpiredError() {
		t.Error("expected no injection when disabled")
	}
}

func TestShouldInjectTokenExpiredError_ZeroRate(t *testing.T) {
	inj := NewInjector(types.ErrorInjectionConfig{
		Enabled:               true,
		TokenExpiredErrorRate: 0.0,
	})

	if inj.ShouldInjectTokenExpiredError() {
		t.Error("expected no injection with zero rate")
	}
}

