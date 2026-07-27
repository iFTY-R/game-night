package adminauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	transporterrors "github.com/iFTY-R/game-night/apps/api/internal/transport/errors"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/origin"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/proxy"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/sensitive"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1/adminv1connect"
	"github.com/iFTY-R/game-night/platform/admin"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

func TestContextInterceptorRejectsUnknownProcedure(t *testing.T) {
	interceptor := newInterceptor(t, &fakeSessionInspector{})
	called := false
	handler := connect.NewUnaryHandler(
		"/platform.admin.v1.AdminAuthService/Unknown",
		func(context.Context, *connect.Request[adminv1.GetSetupStateRequest]) (*connect.Response[adminv1.GetSetupStateResponse], error) {
			called = true
			return connect.NewResponse(&adminv1.GetSetupStateResponse{}), nil
		},
		connect.WithInterceptors(interceptor),
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := connect.NewClient[adminv1.GetSetupStateRequest, adminv1.GetSetupStateResponse](server.Client(), server.URL+"/platform.admin.v1.AdminAuthService/Unknown")

	request := connect.NewRequest(&adminv1.GetSetupStateRequest{})
	request.Header().Set("Origin", "https://admin.example.test")
	_, err := client.CallUnary(t.Context(), request)
	if err == nil {
		t.Fatal("unknown procedure unexpectedly succeeded")
	}
	if called {
		t.Fatal("unknown procedure reached downstream handler")
	}
}

func TestSecondFactorWireReportsAcceptedCredentialPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		usedSecondFactor bool
		usedRecoveryCode bool
		want             adminv1.AdminElevationSecondFactor
	}{
		{name: "password", want: adminv1.AdminElevationSecondFactor_ADMIN_ELEVATION_SECOND_FACTOR_PASSWORD},
		{name: "totp", usedSecondFactor: true, want: adminv1.AdminElevationSecondFactor_ADMIN_ELEVATION_SECOND_FACTOR_TOTP},
		{name: "recovery", usedSecondFactor: true, usedRecoveryCode: true, want: adminv1.AdminElevationSecondFactor_ADMIN_ELEVATION_SECOND_FACTOR_RECOVERY_CODE},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := secondFactorWire(test.usedSecondFactor, test.usedRecoveryCode); got != test.want {
				t.Fatalf("second factor = %v, want %v", got, test.want)
			}
		})
	}
}

func TestContextInterceptorAllowsAnonymousBeginLoginWithoutSessionLookup(t *testing.T) {
	inspector := &fakeSessionInspector{}
	interceptor := newInterceptor(t, inspector)
	called := false
	handler := connect.NewUnaryHandler(
		"/platform.admin.v1.AdminAuthService/BeginAdminLogin",
		func(context.Context, *connect.Request[adminv1.BeginAdminLoginRequest]) (*connect.Response[adminv1.BeginAdminLoginResponse], error) {
			called = true
			return connect.NewResponse(&adminv1.BeginAdminLoginResponse{}), nil
		},
		connect.WithInterceptors(interceptor),
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := connect.NewClient[adminv1.BeginAdminLoginRequest, adminv1.BeginAdminLoginResponse](server.Client(), server.URL+"/platform.admin.v1.AdminAuthService/BeginAdminLogin")

	request := connect.NewRequest(&adminv1.BeginAdminLoginRequest{RequestFlowId: "flow-1"})
	request.Header().Set("Origin", "https://admin.example.test")
	_, err := client.CallUnary(t.Context(), request)
	if err != nil {
		t.Fatalf("BeginAdminLogin error = %v", err)
	}
	if !called {
		t.Fatal("anonymous begin-login request did not reach downstream handler")
	}
	if inspector.resolveCalls != 0 || inspector.currentCalls != 0 {
		t.Fatalf("anonymous request unexpectedly inspected a session: resolve=%d current=%d", inspector.resolveCalls, inspector.currentCalls)
	}
}

func TestContextInterceptorSeparatesPreviewAndBulkRevokePolicies(t *testing.T) {
	now := time.Date(2026, time.July, 25, 13, 0, 0, 0, time.UTC)
	issued, view := newSessionView(t, now, admin.SessionKindFull, false)
	inspector := &fakeSessionInspector{session: issued.Session, view: view}
	interceptor := newInterceptor(t, inspector)

	previewCalled := false
	previewHandler := connect.NewUnaryHandler(
		"/platform.admin.v1.AdminAuthService/PreviewRevokeOtherAdminSessions",
		func(context.Context, *connect.Request[adminv1.PreviewRevokeOtherAdminSessionsRequest]) (*connect.Response[adminv1.PreviewRevokeOtherAdminSessionsResponse], error) {
			previewCalled = true
			return connect.NewResponse(&adminv1.PreviewRevokeOtherAdminSessionsResponse{}), nil
		},
		connect.WithInterceptors(interceptor),
	)
	previewServer := httptest.NewServer(previewHandler)
	t.Cleanup(previewServer.Close)
	previewClient := connect.NewClient[adminv1.PreviewRevokeOtherAdminSessionsRequest, adminv1.PreviewRevokeOtherAdminSessionsResponse](
		previewServer.Client(),
		previewServer.URL+"/platform.admin.v1.AdminAuthService/PreviewRevokeOtherAdminSessions",
	)
	previewRequest := connect.NewRequest(&adminv1.PreviewRevokeOtherAdminSessionsRequest{})
	previewRequest.Header().Set("Origin", "https://admin.example.test")
	previewRequest.Header().Set(csrf.HeaderName, issued.CSRFToken)
	previewRequest.Header().Add("Cookie", cookies.AdminSessionCookieName+"="+issued.Token+"; "+cookies.AdminCSRFCookieName+"="+issued.CSRFToken)
	if _, err := previewClient.CallUnary(t.Context(), previewRequest); err != nil {
		t.Fatalf("preview revoke-other error = %v", err)
	}
	if !previewCalled {
		t.Fatal("preview revoke-other did not reach downstream handler")
	}

	revokeCalled := false
	revokeHandler := connect.NewUnaryHandler(
		"/platform.admin.v1.AdminAuthService/RevokeOtherAdminSessions",
		func(context.Context, *connect.Request[adminv1.RevokeOtherAdminSessionsRequest]) (*connect.Response[adminv1.RevokeOtherAdminSessionsResponse], error) {
			revokeCalled = true
			return connect.NewResponse(&adminv1.RevokeOtherAdminSessionsResponse{}), nil
		},
		connect.WithInterceptors(interceptor),
	)
	revokeServer := httptest.NewServer(revokeHandler)
	t.Cleanup(revokeServer.Close)
	revokeClient := connect.NewClient[adminv1.RevokeOtherAdminSessionsRequest, adminv1.RevokeOtherAdminSessionsResponse](
		revokeServer.Client(),
		revokeServer.URL+"/platform.admin.v1.AdminAuthService/RevokeOtherAdminSessions",
	)
	revokeRequest := connect.NewRequest(&adminv1.RevokeOtherAdminSessionsRequest{
		OperationId:                   "admin-op-1",
		PreviewVersion:                "preview-v1",
		ExpectedAdminVersion:          1,
		ExpectedCurrentSessionVersion: 1,
	})
	revokeRequest.Header().Set("Origin", "https://admin.example.test")
	revokeRequest.Header().Set(csrf.HeaderName, issued.CSRFToken)
	revokeRequest.Header().Set(RequestIDHeader, "request-1")
	revokeRequest.Header().Add("Cookie", cookies.AdminSessionCookieName+"="+issued.Token+"; "+cookies.AdminCSRFCookieName+"="+issued.CSRFToken)
	_, err := revokeClient.CallUnary(t.Context(), revokeRequest)
	if err == nil {
		t.Fatal("bulk revoke unexpectedly succeeded without elevation")
	}
	if revokeCalled {
		t.Fatal("bulk revoke reached downstream handler without elevation")
	}
}

func TestGetCurrentAdminSessionReturnsSummaryWithoutLegacyStepState(t *testing.T) {
	now := time.Date(2026, time.July, 25, 15, 0, 0, 0, time.UTC)
	manager, err := cookies.NewManager(clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	effects, err := NewCookieEffects(manager)
	if err != nil {
		t.Fatal(err)
	}
	issued, service := newHandlerService(t, now, admin.SessionKindFull)
	registry, err := sensitive.New(sensitive.AllOperations()...)
	if err != nil {
		t.Fatal(err)
	}
	interceptor := newInterceptor(t, service)
	options := []connect.HandlerOption{connect.WithInterceptors(registry.Interceptor(), transporterrors.Interceptor(), interceptor)}
	path, handler, err := NewHandler(service, effects, testReadiness(t, true), options...)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := adminv1connect.NewAdminAuthServiceClient(server.Client(), server.URL)

	request := connect.NewRequest(&adminv1.GetCurrentAdminSessionRequest{})
	request.Header().Set("Origin", "https://admin.example.test")
	request.Header().Set(csrf.HeaderName, issued.CSRFToken)
	request.Header().Add("Cookie", cookies.AdminSessionCookieName+"="+issued.Token+"; "+cookies.AdminCSRFCookieName+"="+issued.CSRFToken)
	response, err := client.GetCurrentAdminSession(t.Context(), request)
	if err != nil {
		t.Fatalf("GetCurrentAdminSession error = %v", err)
	}
	if response.Msg.GetSession().GetKind() != adminv1.AdminSessionKind_ADMIN_SESSION_KIND_FULL {
		t.Fatalf("session kind = %s", response.Msg.GetSession().GetKind())
	}
	if response.Msg.GetSession().GetSessionId() == "" || response.Msg.GetSession().GetAdminId() == "" {
		t.Fatalf("session summary = %+v", response.Msg.GetSession())
	}
	if len(response.Msg.GetSession().GetPermissions()) == 0 {
		t.Fatalf("session permissions = %+v", response.Msg.GetSession())
	}
	if values := response.Header().Values("Set-Cookie"); len(values) != 0 {
		t.Fatalf("unexpected Set-Cookie = %v", values)
	}
}

func TestElevateAdminSessionAllowsPasswordOnlyRequestWithoutMFAEnrollment(t *testing.T) {
	now := time.Date(2026, time.July, 27, 16, 55, 0, 0, time.UTC)
	issued, service := newHandlerService(t, now, admin.SessionKindFull)
	view := admin.SessionView{
		Session:     issued.Session,
		Permissions: admin.ActiveAdminPermissionSet(),
	}
	actor, err := actorFromView(view, "request-elevation", "https://admin.example.test", "127.0.0.1", "handler-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := withRequestContext(t.Context(), requestContext{
		transport: transportContext{
			cookieToken: issued.Token,
			csrfToken:   issued.CSRFToken,
			clientIP:    "127.0.0.1",
			requestID:   "request-elevation",
		},
		view:  &view,
		actor: &actor,
	})
	request := connect.NewRequest(&adminv1.ElevateAdminSessionRequest{
		OperationId:     base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)),
		Scope:           adminv1.AdminElevationScope_ADMIN_ELEVATION_SCOPE_OPERATIONS_MAINTENANCE,
		CurrentPassword: "wrong-password",
	})

	_, err = (&Handler{service: service}).ElevateAdminSession(ctx, request)
	if !errors.Is(err, admin.ErrAuthentication) {
		t.Fatalf("ElevateAdminSession error = %v, want authentication failure after password verification", err)
	}
}

type fakeSessionInspector struct {
	session      admin.Session
	view         admin.SessionView
	resolveErr   error
	currentErr   error
	resolveCalls int
	currentCalls int
}

func (inspector *fakeSessionInspector) ResolveSession(context.Context, string) (admin.Session, error) {
	inspector.resolveCalls++
	return inspector.session, inspector.resolveErr
}

func (inspector *fakeSessionInspector) GetCurrentAdminSession(context.Context, admin.CurrentSessionCommand) (admin.CurrentSessionResult, error) {
	inspector.currentCalls++
	return admin.CurrentSessionResult{View: inspector.view}, inspector.currentErr
}

func newInterceptor(t testing.TB, inspector sessionInspector) *ContextInterceptor {
	t.Helper()
	origins, err := origin.NewAdminValidator(sharedconfig.OriginAllowlist{"https://admin.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	clients, err := proxy.NewResolver([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	interceptor, err := NewContextInterceptor(inspector, origins, csrf.NewAdminValidator(), clients)
	if err != nil {
		t.Fatal(err)
	}
	return interceptor
}

func newSessionView(t testing.TB, now time.Time, kind admin.SessionKind, includeRevokeElevation bool) (admin.IssuedSession, admin.SessionView) {
	t.Helper()
	keyring := loadAdminSessionHMACKeyring(t, now)
	sessionService, err := admin.NewSessionService(keyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	adminID := uuid.Must(uuid.NewV7())
	issued, err := sessionService.IssueWithClient(
		adminID,
		kind,
		1,
		1,
		admin.SessionClientMetadata{ClientIP: "127.0.0.1", UserAgent: "policy-test"},
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	view := admin.SessionView{Session: issued.Session}
	if kind == admin.SessionKindFull {
		view.Permissions = admin.ActiveAdminPermissionSet()
	}
	if includeRevokeElevation {
		enrollment := newActiveEnrollment(t, adminID, 1, 2, now)
		elevation, err := admin.NewElevation(issued.Session, enrollment.Snapshot().EnrollmentVersion, admin.ElevationScopeSecurityRevokeSessions, now, now.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		elevations, err := admin.NewElevationSet(elevation)
		if err != nil {
			t.Fatal(err)
		}
		view.Enrollment = &enrollment
		view.Elevations = elevations
		view.RecoveryCodes = admin.RecoveryCodeSetState{SetVersion: 1, RemainingActive: 8}
	}
	return issued, view
}

func newActiveEnrollment(t testing.TB, adminID uuid.UUID, adminVersion int64, enrollmentVersion int64, now time.Time) admin.Enrollment {
	t.Helper()
	replayFloor := int64(1)
	enrollment, err := admin.RestoreEnrollment(admin.EnrollmentSnapshot{
		ID:                uuid.Must(uuid.NewV7()),
		AdminID:           adminID,
		Ciphertext:        []byte{1},
		Nonce:             []byte{1},
		KeyVersion:        1,
		Status:            admin.EnrollmentStatusActive,
		AdminVersion:      adminVersion,
		EnrollmentVersion: enrollmentVersion,
		ReplayFloor:       &replayFloor,
		OperationID:       "op-1",
		CreatedAt:         now.Add(-time.Hour),
		ActivatedAt:       now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return enrollment
}

func newHandlerService(t testing.TB, now time.Time, kind admin.SessionKind) (admin.IssuedSession, *admin.Service) {
	t.Helper()
	keyring := loadAdminSessionHMACKeyring(t, now)
	sessionService, err := admin.NewSessionService(keyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	account, err := admin.RestoreAccount(admin.AccountSnapshot{
		ID:                 uuid.Must(uuid.NewV7()),
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
	issued, err := sessionService.IssueWithClient(
		account.Snapshot().ID,
		kind,
		account.Snapshot().AdminVersion,
		account.Snapshot().PasswordVersion,
		admin.SessionClientMetadata{ClientIP: "127.0.0.1", UserAgent: "handler-test"},
		now,
	)
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
		UnitOfWork: handlerUnitOfWork{
			accounts: handlerAccountRepository{account: account},
			sessions: handlerSessionRepository{session: issued.Session},
		},
		Limiter: dummyRateLimiter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return issued, service
}

func testReadiness(t testing.TB, redisReady bool) *server.Readiness {
	t.Helper()
	ready := server.CheckFunc(func(context.Context) error { return nil })
	redis := server.CheckFunc(func(context.Context) error {
		if redisReady {
			return nil
		}
		return connect.NewError(connect.CodeUnavailable, errors.New("redis unavailable"))
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

type handlerUnitOfWork struct {
	accounts handlerAccountRepository
	sessions handlerSessionRepository
}

func (unitOfWork handlerUnitOfWork) Run(ctx context.Context, work admin.TransactionWork) error {
	return work(ctx, handlerTransaction{accounts: unitOfWork.accounts, sessions: unitOfWork.sessions})
}

type handlerTransaction struct {
	accounts handlerAccountRepository
	sessions handlerSessionRepository
}

func (transaction handlerTransaction) Challenges() admin.ChallengeRepository           { return nil }
func (transaction handlerTransaction) SecretResults() secretresult.Repository          { return nil }
func (transaction handlerTransaction) Accounts() admin.AccountRepository               { return transaction.accounts }
func (transaction handlerTransaction) Enrollments() admin.EnrollmentRepository         { return nil }
func (transaction handlerTransaction) Sessions() admin.SessionRepository               { return transaction.sessions }
func (transaction handlerTransaction) Elevations() admin.ElevationRepository           { return nil }
func (transaction handlerTransaction) CommandReceipts() admin.CommandReceiptRepository { return nil }
func (transaction handlerTransaction) RecoveryCodes() admin.RecoveryCodeRepository     { return nil }
func (transaction handlerTransaction) Audit() audit.Repository                         { return nil }
func (transaction handlerTransaction) AuditCheckpoints() audit.CheckpointRepository    { return nil }
func (transaction handlerTransaction) OutboxEvents() outbox.EventRepository            { return nil }

type handlerAccountRepository struct {
	account admin.Account
}

func (repository handlerAccountRepository) GetForUpdate(context.Context) (admin.Account, error) {
	return repository.account, nil
}

func (handlerAccountRepository) BootstrapPasswordCAS(context.Context, admin.Account, string, string, string, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}

func (handlerAccountRepository) UpdatePasswordCAS(context.Context, admin.Account, string, string, string, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}

func (handlerAccountRepository) TransitionStatusCAS(context.Context, admin.Account, admin.AccountStatus, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}

func (handlerAccountRepository) RecordMFAChangeCAS(context.Context, admin.Account, time.Time) (admin.Account, error) {
	return admin.Account{}, admin.ErrConcurrentTransition
}

type handlerSessionRepository struct {
	session admin.Session
}

func (handlerSessionRepository) Insert(context.Context, admin.Session) error { return nil }

func (repository handlerSessionRepository) GetForUpdate(context.Context, string) (admin.Session, error) {
	return repository.session, nil
}

func (repository handlerSessionRepository) GetByIDForUpdate(context.Context, uuid.UUID) (admin.Session, error) {
	return repository.session, nil
}

func (repository handlerSessionRepository) ListActiveForAdmin(context.Context, uuid.UUID, time.Time) ([]admin.Session, error) {
	return []admin.Session{repository.session}, nil
}

func (handlerSessionRepository) TouchCAS(context.Context, admin.Session, time.Time, time.Duration) (admin.Session, error) {
	return admin.Session{}, admin.ErrConcurrentTransition
}

func (handlerSessionRepository) RevokeCAS(context.Context, admin.Session, string, time.Time) (admin.Session, error) {
	return admin.Session{}, admin.ErrConcurrentTransition
}

func (handlerSessionRepository) RevokeOtherActiveCAS(context.Context, uuid.UUID, uuid.UUID, int64, int64, string, time.Time) ([]admin.Session, error) {
	return nil, admin.ErrConcurrentTransition
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
	if err = os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	keyring, err := security.LoadHMACKeyring[security.AdminSessionKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}
