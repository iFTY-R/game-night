package adminauth_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/server"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminauth"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/adminidentity"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/sensitive"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestEveryAdminRPCIsImplementedWithStableErrorDetails(t *testing.T) {
	authClient, identityClient := invalidRequestClients(t)
	calls := map[string]func() error{
		"GetSetupState": func() error {
			_, err := authClient.GetSetupState(t.Context(), connect.NewRequest(&adminv1.GetSetupStateRequest{}))
			return err
		},
		"GetCurrentAdminSession": func() error {
			_, err := authClient.GetCurrentAdminSession(t.Context(), connect.NewRequest(&adminv1.GetCurrentAdminSessionRequest{}))
			return err
		},
		"GetRuntimeReadiness": func() error {
			_, err := authClient.GetRuntimeReadiness(t.Context(), connect.NewRequest(&adminv1.GetRuntimeReadinessRequest{}))
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

func TestGetCurrentAdminSessionSucceedsWithoutRequestIDAndDoesNotSetCookie(t *testing.T) {
	authClient, issued := validCurrentSessionClient(t, admin.SessionKindFull)
	request := connect.NewRequest(&adminv1.GetCurrentAdminSessionRequest{})
	request.Header().Set("Origin", "https://admin.example.test")
	request.Header().Set(csrf.HeaderName, issued.CSRFToken)
	request.Header().Add("Cookie", cookies.AdminSessionCookieName+"="+issued.Token+"; "+cookies.AdminCSRFCookieName+"="+issued.CSRFToken)

	response, err := authClient.GetCurrentAdminSession(t.Context(), request)
	if err != nil {
		t.Fatalf("GetCurrentAdminSession error = %v", err)
	}
	if response.Msg.GetNextStep() != adminv1.AdminNextStep_ADMIN_NEXT_STEP_AUTHENTICATED {
		t.Fatalf("next step = %s", response.Msg.GetNextStep())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
	if values := response.Header().Values("Set-Cookie"); len(values) != 0 {
		t.Fatalf("unexpected Set-Cookie = %v", values)
	}
}

func TestGetRuntimeReadinessRequiresFullSessionAndReturnsBoundedComponents(t *testing.T) {
	authClient, issued := validCurrentSessionClient(t, admin.SessionKindFull)
	request := connect.NewRequest(&adminv1.GetRuntimeReadinessRequest{})
	request.Header().Set("Origin", "https://admin.example.test")
	request.Header().Set(csrf.HeaderName, issued.CSRFToken)
	request.Header().Add("Cookie", cookies.AdminSessionCookieName+"="+issued.Token+"; "+cookies.AdminCSRFCookieName+"="+issued.CSRFToken)

	response, err := authClient.GetRuntimeReadiness(t.Context(), request)
	if err != nil {
		t.Fatalf("GetRuntimeReadiness error = %v", err)
	}
	ordinary, sensitive := response.Msg.GetOrdinary(), response.Msg.GetSensitive()
	if ordinary.GetMode() != "ordinary" || !ordinary.GetReady() {
		t.Fatalf("ordinary readiness = %+v", ordinary)
	}
	if sensitive.GetMode() != "sensitive_write" || sensitive.GetReady() {
		t.Fatalf("sensitive readiness = %+v", sensitive)
	}
	if ordinary.GetComponents()["redis"] != "unavailable" || sensitive.GetComponents()["postgresql"] != "ready" {
		t.Fatalf("readiness components are not bounded wire states: ordinary=%v sensitive=%v", ordinary.GetComponents(), sensitive.GetComponents())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
}

func TestGetRuntimeReadinessRejectsPendingAdminSession(t *testing.T) {
	authClient, issued := validCurrentSessionClient(t, admin.SessionKindMFAPending)
	request := connect.NewRequest(&adminv1.GetRuntimeReadinessRequest{})
	request.Header().Set("Origin", "https://admin.example.test")
	request.Header().Set(csrf.HeaderName, issued.CSRFToken)
	request.Header().Add("Cookie", cookies.AdminSessionCookieName+"="+issued.Token+"; "+cookies.AdminCSRFCookieName+"="+issued.CSRFToken)

	_, err := authClient.GetRuntimeReadiness(t.Context(), request)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("pending session error = %v", err)
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
	registry, err := sensitive.New(sensitive.AllOperations()...)
	if err != nil {
		t.Fatal(err)
	}
	options := []connect.HandlerOption{connect.WithInterceptors(registry.Interceptor(), transporterrors.Interceptor(), contextInterceptor)}
	authPath, authHandler, err := adminauth.NewHandler(&admin.Service{}, effects, testReadiness(t, true), options...)
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

func validCurrentSessionClient(t testing.TB, kind admin.SessionKind) (adminv1connect.AdminAuthServiceClient, admin.IssuedSession) {
	t.Helper()

	now := time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC)
	manager, err := cookies.NewManager(clock.NewFake(now))
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
	registry, err := sensitive.New(sensitive.AllOperations()...)
	if err != nil {
		t.Fatal(err)
	}

	sessionKeyring := loadAdminSessionHMACKeyring(t, now)
	sessionService, err := admin.NewSessionService(sessionKeyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	account, err := admin.RestoreAccount(admin.AccountSnapshot{
		ID:                 uuid.New(),
		Username:           "admin",
		Status:             admin.AccountStatusActive,
		PasswordHash:       "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		PasswordAlgorithm:  admin.PasswordAlgorithmArgon2id,
		PasswordParameters: `{"MemoryKiB":65536,"Iterations":3,"Parallelism":2,"SaltLength":16,"KeyLength":32}`,
		PasswordVersion:    2,
		AdminVersion:       3,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := sessionService.Issue(account.Snapshot().ID, kind, account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion, now)
	if err != nil {
		t.Fatal(err)
	}
	service, err := admin.NewService(admin.ServiceDependencies{
		Challenge:      &admin.ChallengeService{},
		Passwords:      dummyPasswordHasher{},
		PasswordPolicy: admin.DefaultPasswordPolicy(),
		TOTP:           &admin.TOTPService{},
		Sessions:       sessionService,
		RecoveryCodes:  &admin.RecoveryCodeService{},
		Results:        &secretresult.Service{},
		Clock:          clock.NewFake(now),
		UnitOfWork: adminHandlerUnitOfWork{
			accounts: adminHandlerAccountRepository{account: account},
			sessions: adminHandlerSessionRepository{session: issued.Session},
		},
		Limiter: dummyRateLimiter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	options := []connect.HandlerOption{connect.WithInterceptors(registry.Interceptor(), transporterrors.Interceptor(), contextInterceptor)}
	path, handler, err := adminauth.NewHandler(service, effects, testReadiness(t, false), options...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return adminv1connect.NewAdminAuthServiceClient(server.Client(), server.URL), issued
}

func testReadiness(t testing.TB, redisReady bool) *server.Readiness {
	t.Helper()
	ready := server.CheckFunc(func(context.Context) error { return nil })
	redis := server.CheckFunc(func(context.Context) error {
		if redisReady {
			return nil
		}
		return stderrors.New("redis unavailable")
	})
	readiness, err := server.NewReadiness(server.ReadinessChecks{
		PostgreSQL: ready,
		Redis:      redis,
		Keyring:    ready,
		Bootstrap:  ready,
		Checkpoint: ready,
	})
	if err != nil {
		t.Fatal(err)
	}
	return readiness
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

type dummyPasswordHasher struct{}

func (dummyPasswordHasher) Hash(context.Context, []byte) (string, error) {
	return "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil
}
func (dummyPasswordHasher) VerifyOrDummy(context.Context, string, []byte) (bool, bool, error) {
	return false, false, nil
}

type dummyRateLimiter struct{}

func (dummyRateLimiter) Consume(context.Context, ratelimit.ConsumptionRequest) (ratelimit.Decision, error) {
	return ratelimit.Allow(), nil
}

type adminHandlerUnitOfWork struct {
	accounts adminHandlerAccountRepository
	sessions adminHandlerSessionRepository
}

func (unitOfWork adminHandlerUnitOfWork) Run(ctx context.Context, work admin.TransactionWork) error {
	return work(ctx, adminHandlerTransaction{accounts: unitOfWork.accounts, sessions: unitOfWork.sessions})
}

type adminHandlerTransaction struct {
	accounts adminHandlerAccountRepository
	sessions adminHandlerSessionRepository
}

func (transaction adminHandlerTransaction) Challenges() admin.ChallengeRepository  { return nil }
func (transaction adminHandlerTransaction) SecretResults() secretresult.Repository { return nil }
func (transaction adminHandlerTransaction) Accounts() admin.AccountRepository {
	return transaction.accounts
}
func (transaction adminHandlerTransaction) Enrollments() admin.EnrollmentRepository { return nil }
func (transaction adminHandlerTransaction) Sessions() admin.SessionRepository {
	return transaction.sessions
}
func (transaction adminHandlerTransaction) RecoveryCodes() admin.RecoveryCodeRepository { return nil }
func (transaction adminHandlerTransaction) IdentityUsers() admin.IdentityUserRepository { return nil }
func (transaction adminHandlerTransaction) IdentityUsernameClaims() identity.UsernameClaimRepository {
	return nil
}
func (transaction adminHandlerTransaction) IdentityDevices() identity.DeviceRepository { return nil }
func (transaction adminHandlerTransaction) IdentityRecoveryCredentials() identity.RecoveryCredentialRepository {
	return nil
}
func (transaction adminHandlerTransaction) Profiles() profile.Repository             { return nil }
func (transaction adminHandlerTransaction) ProfileExports() profile.ExportRepository { return nil }
func (transaction adminHandlerTransaction) AssistedRecoveryGrants() admin.AssistedRecoveryGrantRepository {
	return nil
}
func (transaction adminHandlerTransaction) Audit() audit.Repository                      { return nil }
func (transaction adminHandlerTransaction) AuditCheckpoints() audit.CheckpointRepository { return nil }
func (transaction adminHandlerTransaction) OutboxEvents() outbox.EventRepository         { return nil }

type adminHandlerAccountRepository struct {
	account admin.Account
}

func (repository adminHandlerAccountRepository) GetForUpdate(context.Context) (admin.Account, error) {
	return repository.account, nil
}
func (adminHandlerAccountRepository) BootstrapPasswordCAS(context.Context, admin.Account, string, string, string, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}
func (adminHandlerAccountRepository) UpdatePasswordCAS(context.Context, admin.Account, string, string, string, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}
func (adminHandlerAccountRepository) TransitionStatusCAS(context.Context, admin.Account, admin.AccountStatus, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}
func (adminHandlerAccountRepository) AcceptTOTPStepCAS(context.Context, admin.Account, int64, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}

type adminHandlerSessionRepository struct {
	session admin.Session
}

func (repository adminHandlerSessionRepository) Insert(context.Context, admin.Session) error {
	return nil
}
func (repository adminHandlerSessionRepository) GetForUpdate(context.Context, string) (admin.Session, error) {
	return repository.session, nil
}
func (adminHandlerSessionRepository) TouchCAS(context.Context, admin.Session, time.Time, time.Duration) (admin.Session, error) {
	return admin.Session{}, admin.ErrConcurrentTransition
}
func (adminHandlerSessionRepository) RevokeCAS(context.Context, admin.Session, string, time.Time) (admin.Session, error) {
	return admin.Session{}, admin.ErrConcurrentTransition
}
func (adminHandlerSessionRepository) RevokeAll(context.Context, uuid.UUID, string, time.Time) (int64, error) {
	return 0, admin.ErrConcurrentTransition
}

func loadAdminSessionHMACKeyring(t testing.TB, now time.Time) *security.HMACKeyring[security.AdminSessionKeyPurpose] {
	t.Helper()

	key := bytes.Repeat([]byte{7}, 32)
	document := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version":    1,
			"key":        base64.StdEncoding.EncodeToString(key),
			"not_before": now.Add(-time.Hour),
		}},
	}
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "admin-session-keyring.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	keyring, err := security.LoadHMACKeyring[security.AdminSessionKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
