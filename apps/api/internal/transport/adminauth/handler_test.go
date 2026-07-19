package adminauth_test

import (
	"bytes"
	"encoding/base64"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminidentity"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/clock"
)

func TestEveryAdminRPCIsImplementedWithStableErrorDetails(t *testing.T) {
	authClient, identityClient := invalidRequestClients(t)
	calls := map[string]func() error{
		"GetSetupState": func() error {
			_, err := authClient.GetSetupState(t.Context(), connect.NewRequest(&adminv1.GetSetupStateRequest{}))
			return err
		},
		"BeginAdminLogin": func() error {
			_, err := authClient.BeginAdminLogin(t.Context(), connect.NewRequest(&adminv1.BeginAdminLoginRequest{}))
			return err
		},
		"LoginPassword": func() error {
			_, err := authClient.LoginPassword(t.Context(), connect.NewRequest(&adminv1.LoginPasswordRequest{}))
			return err
		},
		"VerifyTotp": func() error {
			_, err := authClient.VerifyTotp(t.Context(), connect.NewRequest(&adminv1.VerifyTotpRequest{}))
			return err
		},
		"ChangeInitialPassword": func() error {
			_, err := authClient.ChangeInitialPassword(t.Context(), connect.NewRequest(&adminv1.ChangeInitialPasswordRequest{}))
			return err
		},
		"BeginTotpEnrollment": func() error {
			_, err := authClient.BeginTotpEnrollment(t.Context(), connect.NewRequest(&adminv1.BeginTotpEnrollmentRequest{}))
			return err
		},
		"CompleteTotpEnrollment": func() error {
			_, err := authClient.CompleteTotpEnrollment(t.Context(), connect.NewRequest(&adminv1.CompleteTotpEnrollmentRequest{}))
			return err
		},
		"ConfirmAdminSecretReceipt": func() error {
			_, err := authClient.ConfirmAdminSecretReceipt(t.Context(), connect.NewRequest(&adminv1.ConfirmAdminSecretReceiptRequest{}))
			return err
		},
		"RecoverAdmin": func() error {
			_, err := authClient.RecoverAdmin(t.Context(), connect.NewRequest(&adminv1.RecoverAdminRequest{}))
			return err
		},
		"ChangeAdminPassword": func() error {
			_, err := authClient.ChangeAdminPassword(t.Context(), connect.NewRequest(&adminv1.ChangeAdminPasswordRequest{}))
			return err
		},
		"BeginTotpRebind": func() error {
			_, err := authClient.BeginTotpRebind(t.Context(), connect.NewRequest(&adminv1.BeginTotpRebindRequest{}))
			return err
		},
		"CompleteTotpRebind": func() error {
			_, err := authClient.CompleteTotpRebind(t.Context(), connect.NewRequest(&adminv1.CompleteTotpRebindRequest{}))
			return err
		},
		"RegenerateAdminRecoveryCodes": func() error {
			_, err := authClient.RegenerateAdminRecoveryCodes(t.Context(), connect.NewRequest(&adminv1.RegenerateAdminRecoveryCodesRequest{}))
			return err
		},
		"LogoutAdmin": func() error {
			_, err := authClient.LogoutAdmin(t.Context(), connect.NewRequest(&adminv1.LogoutAdminRequest{}))
			return err
		},
		"LogoutAllAdminSessions": func() error {
			_, err := authClient.LogoutAllAdminSessions(t.Context(), connect.NewRequest(&adminv1.LogoutAllAdminSessionsRequest{}))
			return err
		},
		"GetUser": func() error {
			_, err := identityClient.GetUser(t.Context(), connect.NewRequest(&adminv1.GetUserRequest{}))
			return err
		},
		"GetRealName": func() error {
			_, err := identityClient.GetRealName(t.Context(), connect.NewRequest(&adminv1.GetRealNameRequest{}))
			return err
		},
		"UpdateRealName": func() error {
			_, err := identityClient.UpdateRealName(t.Context(), connect.NewRequest(&adminv1.UpdateRealNameRequest{}))
			return err
		},
		"CreateUserProfileExport": func() error {
			_, err := identityClient.CreateUserProfileExport(t.Context(), connect.NewRequest(&adminv1.CreateUserProfileExportRequest{}))
			return err
		},
		"GetUserProfileExportPage": func() error {
			_, err := identityClient.GetUserProfileExportPage(t.Context(), connect.NewRequest(&adminv1.GetUserProfileExportPageRequest{}))
			return err
		},
		"CompleteUserProfileExport": func() error {
			_, err := identityClient.CompleteUserProfileExport(t.Context(), connect.NewRequest(&adminv1.CompleteUserProfileExportRequest{}))
			return err
		},
		"AbortUserProfileExport": func() error {
			_, err := identityClient.AbortUserProfileExport(t.Context(), connect.NewRequest(&adminv1.AbortUserProfileExportRequest{}))
			return err
		},
		"CreateAssistedRecoveryGrant": func() error {
			_, err := identityClient.CreateAssistedRecoveryGrant(t.Context(), connect.NewRequest(&adminv1.CreateAssistedRecoveryGrantRequest{}))
			return err
		},
		"ForceChangeUsername": func() error {
			_, err := identityClient.ForceChangeUsername(t.Context(), connect.NewRequest(&adminv1.ForceChangeUsernameRequest{}))
			return err
		},
		"SuspendUser": func() error {
			_, err := identityClient.SuspendUser(t.Context(), connect.NewRequest(&adminv1.SuspendUserRequest{}))
			return err
		},
		"UnsuspendUser": func() error {
			_, err := identityClient.UnsuspendUser(t.Context(), connect.NewRequest(&adminv1.UnsuspendUserRequest{}))
			return err
		},
		"DeleteUser": func() error {
			_, err := identityClient.DeleteUser(t.Context(), connect.NewRequest(&adminv1.DeleteUserRequest{}))
			return err
		},
		"RevokeUserDevice": func() error {
			_, err := identityClient.RevokeUserDevice(t.Context(), connect.NewRequest(&adminv1.RevokeUserDeviceRequest{}))
			return err
		},
		"ListAuditEvents": func() error {
			_, err := identityClient.ListAuditEvents(t.Context(), connect.NewRequest(&adminv1.ListAuditEventsRequest{}))
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil || connect.CodeOf(err) == connect.CodeUnimplemented {
				t.Fatalf("RPC error = %v", err)
			}
			if detail := businessDetail(t, err); detail.GetMessageKey() == "" {
				t.Fatalf("RPC returned empty business detail: %+v", detail)
			}
		})
	}
}

func TestAdminContextRejectsUserCookieNamespace(t *testing.T) {
	authClient, _ := invalidRequestClients(t)
	userToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, csrf.TokenBytes))
	request := connect.NewRequest(&adminv1.VerifyTotpRequest{})
	request.Header().Set("Origin", "https://admin.example.test")
	request.Header().Set(csrf.HeaderName, userToken)
	request.Header().Add("Cookie", cookies.UserDeviceCookieName+"=v1.user.secret; "+cookies.UserCSRFCookieName+"="+userToken)
	_, err := authClient.VerifyTotp(t.Context(), request)
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("cross-domain Cookie error = %v", err)
	}
	if detail := businessDetail(t, err); detail.GetCode() != commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_AUTH_INVALID {
		t.Fatalf("cross-domain business detail = %+v", detail)
	}
}

func invalidRequestClients(t testing.TB) (adminv1connect.AdminAuthServiceClient, adminv1connect.AdminIdentityServiceClient) {
	t.Helper()
	source := clock.NewFake(time.Date(2026, time.July, 19, 13, 0, 0, 0, time.UTC))
	manager, err := cookies.NewManager(source)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := adminauth.NewCookieEffects(manager)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := origin.NewAdminValidator(sharedconfig.OriginAllowlist{"https://admin.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	clients, err := proxy.NewResolver([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	contextInterceptor, err := adminauth.NewContextInterceptor(origins, csrf.NewAdminValidator(), clients)
	if err != nil {
		t.Fatal(err)
	}
	options := []connect.HandlerOption{connect.WithInterceptors(transporterrors.Interceptor(), contextInterceptor)}
	authPath, authHandler, err := adminauth.NewHandler(&admin.Service{}, effects, options...)
	if err != nil {
		t.Fatal(err)
	}
	identityPath, identityHandler, err := adminidentity.NewHandler(&admin.IdentityService{}, &admin.Service{}, options...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(authPath, authHandler)
	mux.Handle(identityPath, identityHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return adminv1connect.NewAdminAuthServiceClient(server.Client(), server.URL), adminv1connect.NewAdminIdentityServiceClient(server.Client(), server.URL)
}

func businessDetail(t testing.TB, err error) *commonv1.BusinessErrorDetail {
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
