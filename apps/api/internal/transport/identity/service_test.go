package identity

import (
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	"github.com/iFTY-R/game-night/platform/clock"
	domain "github.com/iFTY-R/game-night/platform/identity"
)

func TestEveryIdentityRPCIsImplementedWithStableErrorDetails(t *testing.T) {
	service := invalidRequestService(t)
	path, handler := identityv1connect.NewIdentityServiceHandler(service, connect.WithInterceptors(transporterrors.Interceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := identityv1connect.NewIdentityServiceClient(server.Client(), server.URL)

	calls := map[string]func() error{
		"BeginIdentityBootstrap": func() error {
			_, err := client.BeginIdentityBootstrap(t.Context(), connect.NewRequest(&identityv1.BeginIdentityBootstrapRequest{}))
			return err
		},
		"BootstrapIdentity": func() error {
			_, err := client.BootstrapIdentity(t.Context(), connect.NewRequest(&identityv1.BootstrapIdentityRequest{}))
			return err
		},
		"CompleteOnboarding": func() error {
			_, err := client.CompleteOnboarding(t.Context(), connect.NewRequest(&identityv1.CompleteOnboardingRequest{}))
			return err
		},
		"GetCurrentIdentity": func() error {
			_, err := client.GetCurrentIdentity(t.Context(), connect.NewRequest(&identityv1.GetCurrentIdentityRequest{}))
			return err
		},
		"ChangeUsername": func() error {
			_, err := client.ChangeUsername(t.Context(), connect.NewRequest(&identityv1.ChangeUsernameRequest{}))
			return err
		},
		"RotateRecoveryCode": func() error {
			_, err := client.RotateRecoveryCode(t.Context(), connect.NewRequest(&identityv1.RotateRecoveryCodeRequest{}))
			return err
		},
		"BeginRecoveryChallenge": func() error {
			_, err := client.BeginRecoveryChallenge(t.Context(), connect.NewRequest(&identityv1.BeginRecoveryChallengeRequest{}))
			return err
		},
		"BeginRecovery": func() error {
			_, err := client.BeginRecovery(t.Context(), connect.NewRequest(&identityv1.BeginRecoveryRequest{}))
			return err
		},
		"CompleteRecovery": func() error {
			_, err := client.CompleteRecovery(t.Context(), connect.NewRequest(&identityv1.CompleteRecoveryRequest{}))
			return err
		},
		"ConfirmSecretReceipt": func() error {
			_, err := client.ConfirmSecretReceipt(t.Context(), connect.NewRequest(&identityv1.ConfirmSecretReceiptRequest{}))
			return err
		},
		"ListDevices": func() error {
			_, err := client.ListDevices(t.Context(), connect.NewRequest(&identityv1.ListDevicesRequest{}))
			return err
		},
		"RevokeDevice": func() error {
			_, err := client.RevokeDevice(t.Context(), connect.NewRequest(&identityv1.RevokeDeviceRequest{}))
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || connect.CodeOf(err) == connect.CodeUnimplemented {
				t.Fatalf("RPC error = %v", err)
			}
			if detail := errorBusinessDetail(t, err); detail.GetMessageKey() == "" {
				t.Fatalf("RPC returned empty business detail: %+v", detail)
			}
		})
	}
}

func TestAccountInstructionClearsCookiesOnConnectError(t *testing.T) {
	now := time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC)
	manager, err := cookies.NewManager(clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{cookies: manager}
	response := connect.NewResponse(&identityv1.GetCurrentIdentityResponse{})
	err = service.applyDeviceResult(response, domain.DeviceCredential{}, nil, domain.CredentialInstructionClear, domain.AccountInstructionSuspended)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("account error = %v", err)
	}
	var connectError *connect.Error
	if !stderrors.As(err, &connectError) {
		t.Fatalf("account error type = %T", err)
	}
	setCookies := connectError.Meta().Values("Set-Cookie")
	if len(setCookies) != 2 {
		t.Fatalf("clear Cookie headers = %v", setCookies)
	}
	for _, value := range setCookies {
		parsed, parseErr := http.ParseSetCookie(value)
		if parseErr != nil || parsed.MaxAge != -1 || parsed.Value != "" || !parsed.Secure || parsed.Path != "/" {
			t.Fatalf("invalid clear Cookie %q: parsed=%+v err=%v", value, parsed, parseErr)
		}
	}
	detail := errorBusinessDetail(t, err)
	if detail.GetCode() != commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_ACCOUNT_SUSPENDED {
		t.Fatalf("business detail = %+v", detail)
	}
}

func TestDeviceCursorRoundTrip(t *testing.T) {
	want := domain.DevicePageCursor{CreatedAt: time.Date(2026, time.July, 19, 12, 3, 4, 56789, time.UTC), CredentialID: uuid.MustParse("01981e1e-fc00-7000-8000-000000000001")}
	encoded, err := encodeDeviceCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeDeviceCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
	if _, err := decodeDeviceCursor(encoded + "="); !stderrors.Is(err, domain.ErrInvalidIdentityRequest) {
		t.Fatalf("non-canonical cursor error = %v", err)
	}
}

func invalidRequestService(t testing.TB) *Service {
	t.Helper()
	source := clock.NewFake(time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC))
	manager, err := cookies.NewManager(source)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := origin.NewUserValidator(sharedconfig.OriginAllowlist{"https://play.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	clients, err := proxy.NewResolver([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(&domain.Service{}, manager, origins, csrf.NewUserValidator(), clients, source)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func errorBusinessDetail(t testing.TB, err error) *commonv1.BusinessErrorDetail {
	t.Helper()
	var connectError *connect.Error
	if !stderrors.As(err, &connectError) || len(connectError.Details()) != 1 {
		t.Fatalf("Connect details missing: %v", err)
	}
	message, valueErr := connectError.Details()[0].Value()
	if valueErr != nil {
		t.Fatal(valueErr)
	}
	detail, ok := message.(*commonv1.BusinessErrorDetail)
	if !ok {
		t.Fatalf("detail type = %T", message)
	}
	return detail
}
