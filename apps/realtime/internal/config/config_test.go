package config

import (
	"strings"
	"testing"
	"time"

	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
)

func TestLoadProcessConfigUsesBoundedDevelopmentDefaults(t *testing.T) {
	t.Parallel()
	values := map[string]string{internalTokenEnvironment: strings.Repeat("a", 32)}
	config, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentDevelopment)
	if err != nil {
		t.Fatalf("loadProcessConfig() error = %v", err)
	}
	if config.Listener.PublicAddress != defaultPublicListenAddress || config.Listener.InternalAddress != defaultInternalListenAddress {
		t.Fatalf("listener config = %+v", config.Listener)
	}
	if config.Ownership.AdvertisedURL != defaultAdvertisedURL || config.Ownership.LeaseTTL != defaultLeaseTTL ||
		config.Ownership.RenewInterval != defaultRenewInterval {
		t.Fatalf("ownership config = %+v", config.Ownership)
	}
	if config.WebSocket.MaxMessageBytes != defaultMaxMessageBytes ||
		config.WebSocket.SendQueueCapacity != defaultSendQueueCapacity {
		t.Fatalf("WebSocket config = %+v", config.WebSocket)
	}
	if config.Timer.ScanInterval != defaultTimerScanInterval || config.Timer.OperationTimeout != defaultTimerOperationTimeout ||
		config.Timer.BatchSize != defaultTimerBatchSize {
		t.Fatalf("timer config = %+v", config.Timer)
	}
	if config.Fanout.LeaseDuration != defaultOutboxLeaseDuration || config.Fanout.PollInterval != defaultOutboxPollInterval ||
		config.Fanout.BatchSize != defaultOutboxBatchSize {
		t.Fatalf("fanout config = %+v", config.Fanout)
	}
}

func TestLoadProcessConfigRejectsSharedListener(t *testing.T) {
	t.Parallel()
	values := validProcessEnvironment()
	values[internalListenAddressEnvironment] = values[publicListenAddressEnvironment]
	_, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentDevelopment)
	if err == nil || !strings.Contains(err.Error(), internalListenAddressEnvironment) {
		t.Fatalf("loadProcessConfig() error = %v", err)
	}
}

func TestLoadProcessConfigRequiresTLSAdvertisedURLInProduction(t *testing.T) {
	t.Parallel()
	values := validProcessEnvironment()
	values[advertisedURLEnvironment] = "http://realtime.internal:8091"
	_, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentProduction)
	if err == nil || !strings.Contains(err.Error(), advertisedURLEnvironment) {
		t.Fatalf("loadProcessConfig() error = %v", err)
	}
	values[advertisedURLEnvironment] = "https://realtime.internal:8091"
	if _, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentProduction); err != nil {
		t.Fatalf("loadProcessConfig() production TLS error = %v", err)
	}
}

func TestLoadProcessConfigRejectsWeakInternalCredentialWithoutEchoingIt(t *testing.T) {
	t.Parallel()
	values := validProcessEnvironment()
	secret := "short-secret"
	values[internalTokenEnvironment] = secret
	_, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentDevelopment)
	if err == nil || !strings.Contains(err.Error(), internalTokenEnvironment) || strings.Contains(err.Error(), secret) {
		t.Fatalf("loadProcessConfig() error = %v", err)
	}
}

func TestLoadProcessConfigRejectsRenewalTooCloseToExpiry(t *testing.T) {
	t.Parallel()
	values := validProcessEnvironment()
	values[leaseTTLEnvironment] = "10s"
	values[renewIntervalEnvironment] = "6s"
	_, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentDevelopment)
	if err == nil || !strings.Contains(err.Error(), renewIntervalEnvironment) {
		t.Fatalf("loadProcessConfig() error = %v", err)
	}
}

func TestLoadProcessConfigParsesResourceOverrides(t *testing.T) {
	t.Parallel()
	values := validProcessEnvironment()
	values[helloTimeoutEnvironment] = "3s"
	values[maxMessageBytesEnvironment] = "8192"
	values[sendQueueCapacityEnvironment] = "8"
	values[timerScanIntervalEnvironment] = "100ms"
	values[timerOperationTimeoutEnvironment] = "3s"
	values[timerBatchSizeEnvironment] = "64"
	values[outboxLeaseDurationEnvironment] = "20s"
	values[outboxPollIntervalEnvironment] = "500ms"
	values[outboxBatchSizeEnvironment] = "32"
	config, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentDevelopment)
	if err != nil {
		t.Fatalf("loadProcessConfig() error = %v", err)
	}
	if config.WebSocket.HelloTimeout != 3*time.Second || config.WebSocket.MaxMessageBytes != 8192 ||
		config.WebSocket.SendQueueCapacity != 8 {
		t.Fatalf("WebSocket config = %+v", config.WebSocket)
	}
	if config.Timer.ScanInterval != 100*time.Millisecond || config.Timer.OperationTimeout != 3*time.Second || config.Timer.BatchSize != 64 {
		t.Fatalf("timer config = %+v", config.Timer)
	}
	if config.Fanout.LeaseDuration != 20*time.Second || config.Fanout.PollInterval != 500*time.Millisecond || config.Fanout.BatchSize != 32 {
		t.Fatalf("fanout config = %+v", config.Fanout)
	}
}

func TestLoadProcessConfigRejectsUnboundedDurableFanout(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		outboxLeaseDurationEnvironment: "10m",
		outboxPollIntervalEnvironment:  "1ms",
		outboxBatchSizeEnvironment:     "1001",
	} {
		t.Run(name, func(t *testing.T) {
			values := validProcessEnvironment()
			values[name] = value
			_, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentDevelopment)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("loadProcessConfig() error = %v", err)
			}
		})
	}
}

func TestLoadProcessConfigRejectsUnboundedTimerScheduling(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		timerScanIntervalEnvironment:     "1ms",
		timerOperationTimeoutEnvironment: "2h",
		timerBatchSizeEnvironment:        "1025",
	} {
		t.Run(name, func(t *testing.T) {
			values := validProcessEnvironment()
			values[name] = value
			_, err := loadProcessConfig(lookupMap(values), sharedconfig.EnvironmentDevelopment)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("loadProcessConfig() error = %v", err)
			}
		})
	}
}

func validProcessEnvironment() map[string]string {
	return map[string]string{
		publicListenAddressEnvironment:   ":8090",
		internalListenAddressEnvironment: ":8091",
		advertisedURLEnvironment:         "http://realtime.internal:8091",
		instanceIDEnvironment:            "realtime-a",
		internalTokenEnvironment:         strings.Repeat("t", 32),
	}
}

func lookupMap(values map[string]string) sharedconfig.LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
