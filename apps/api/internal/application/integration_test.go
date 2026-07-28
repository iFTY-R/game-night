package application

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/apps/api/internal/bootstrap"
	apiConfig "github.com/iFTY-R/game-night/apps/api/internal/config"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/cookies"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/csrf"
	identitytransport "github.com/iFTY-R/game-night/apps/api/internal/transport/identity"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/logging"
	sharedconfig "github.com/iFTY-R/game-night/apps/internal/config"
	"github.com/iFTY-R/game-night/apps/internal/serviceheartbeat"
	commonv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/common/v1"
	gamev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/game/v1/gamev1connect"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	realtimev1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/realtime/v1/realtimev1connect"
	roomv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/room/v1/roomv1connect"
	"github.com/iFTY-R/game-night/internal/integrationtest"
	"github.com/iFTY-R/game-night/platform/audit"
	gameregistry "github.com/iFTY-R/game-night/tooling/game-registry"
	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Integration values are deliberate markers used to prove configured secrets and PII never enter structured logs.
	applicationTestDatabaseEnvironment = "GAME_NIGHT_TEST_DATABASE_URL"
	applicationTestRedisEnvironment    = "GAME_NIGHT_TEST_REDIS_URL"
	applicationUserOrigin              = "https://play.example.test"
	applicationAdminOrigin             = "https://admin.example.test"
	applicationBootstrapPassword       = "Night-admin-bootstrap-2026!"
)

// TestApplicationConnectIdentityAndGameIntegration exercises the real application graph through browser-style TLS Connect clients.
func TestApplicationConnectIdentityAndGameIntegration(t *testing.T) {
	runtime := newApplicationIntegrationRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	identityClient := identityv1connect.NewIdentityServiceClient(runtime.client, runtime.baseURL)
	identity := onboardAndRecoverIdentity(t, ctx, runtime, identityClient)
	exerciseRoomLifecycle(t, ctx, runtime, roomv1connect.NewRoomServiceClient(runtime.client, runtime.baseURL))
	exerciseUnavailableRealtimeSurface(t, ctx, runtime, gamev1connect.NewGameServiceClient(runtime.client, runtime.baseURL))

	devicesRequest := connect.NewRequest(&identityv1.ListDevicesRequest{IncludeRevoked: true, Page: &commonv1.PageRequest{PageSize: 10}})
	devices, err := identityClient.ListDevices(ctx, devicesRequest)
	if err != nil || len(devices.Msg.GetDevices()) != 2 {
		t.Fatalf("list devices after identity recovery: count=%d err=%v", len(devices.Msg.GetDevices()), err)
	}
	revokeRequest := connect.NewRequest(&identityv1.RevokeDeviceRequest{CredentialId: identity.currentCredentialID, Reason: "user_requested"})
	runtime.authorizeUserWrite(t, revokeRequest)
	revokeRequest.Header().Set(identitytransport.RequestIDHeader, "request-"+uuid.NewString())
	revoked, err := identityClient.RevokeDevice(ctx, revokeRequest)
	if err != nil || !revoked.Msg.GetCurrentDeviceRevoked() {
		t.Fatalf("revoke current device: revoked=%t err=%v", revoked.Msg.GetCurrentDeviceRevoked(), err)
	}
	if strings.Contains(runtime.logs.String(), applicationBootstrapPassword) ||
		strings.Contains(runtime.logs.String(), identity.userID) {
		t.Fatal("application logs contain a configured password or real name")
	}
}

// exerciseUnavailableRealtimeSurface proves registry-enabled GameService fails closed when its private owner service is unavailable.
func exerciseUnavailableRealtimeSurface(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client gamev1connect.GameServiceClient,
) {
	t.Helper()
	request := connect.NewRequest(&gamev1.StartSessionRequest{
		RoomId: uuid.NewString(), GameId: "liars-dice", ExpectedRoomVersion: 1, ExpectedMembershipVersion: 1,
		OperationId:   applicationOperationID(t),
		RequestDigest: bytes.Repeat([]byte{1}, 32),
		Config:        &gamev1.GameConfig{GameId: "liars-dice", SchemaVersion: 1, MessageType: "session.config"},
	})
	runtime.authorizeUserWrite(t, request)
	_, err := client.StartSession(ctx, request)
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("unavailable realtime game service error = %v", err)
	}
	var connectError *connect.Error
	if !errors.As(err, &connectError) || connectError.Meta().Get("Cache-Control") != "no-store" {
		t.Fatalf("unavailable realtime game service cache metadata = %v", err)
	}
}

// exerciseRoomLifecycle proves recovered device authority reaches room PostgreSQL mutations through TLS Connect.
func exerciseRoomLifecycle(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client roomv1connect.RoomServiceClient,
) {
	t.Helper()
	createRequest := connect.NewRequest(&roomv1.CreateRoomRequest{
		Visibility: roomv1.RoomVisibility_ROOM_VISIBILITY_PUBLIC, ParticipantCapacity: 4,
		ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN,
		SpectatorAdmission:   roomv1.AdmissionMode_ADMISSION_MODE_APPROVAL,
	})
	runtime.authorizeUserWrite(t, createRequest)
	created, err := client.CreateRoom(ctx, createRequest)
	if err != nil {
		t.Fatalf("create integrated room: %v", err)
	}
	if created.Msg.GetRoom().GetRoomId() == "" || created.Msg.GetRoom().GetRoomCode() == "" {
		t.Fatalf("create integrated room: room=%+v", created.Msg.GetRoom())
	}
	listed, err := client.ListPublicRooms(ctx, connect.NewRequest(&roomv1.ListPublicRoomsRequest{
		Filter: &roomv1.PublicRoomFilter{Statuses: []roomv1.RoomStatus{roomv1.RoomStatus_ROOM_STATUS_LOBBY}},
		Page:   &commonv1.PageRequest{PageSize: 1},
	}))
	if err != nil || len(listed.Msg.GetRooms()) != 1 {
		t.Fatalf("list integrated public rooms: response=%+v err=%v", listed, err)
	}
	card := listed.Msg.GetRooms()[0]
	if card.GetRoomId() != created.Msg.GetRoom().GetRoomId() || card.GetHostUsername() != "CU09" ||
		card.GetPrimaryAction() != roomv1.PublicRoomPrimaryAction_PUBLIC_ROOM_PRIMARY_ACTION_ENTER_ROOM ||
		card.GetParticipantCount() != 1 || listed.Msg.GetPage().GetNextPageToken() != "" {
		t.Fatalf("list integrated public rooms: card=%+v", card)
	}
	assertNoStore(t, listed.Header())
	loaded, err := client.GetRoom(ctx, connect.NewRequest(&roomv1.GetRoomRequest{RoomId: created.Msg.GetRoom().GetRoomId()}))
	if err != nil {
		t.Fatalf("get integrated room: %v", err)
	}
	if loaded.Msg.GetRoom().GetRoomCode() != created.Msg.GetRoom().GetRoomCode() {
		t.Fatalf("get integrated room: room=%+v", loaded.Msg.GetRoom())
	}
	startRequest := connect.NewRequest(&roomv1.StartGameRequest{
		RoomId: created.Msg.GetRoom().GetRoomId(), GameId: "liars-dice", ExpectedVersion: created.Msg.GetRoom().GetVersion(),
		Config: &gamev1.GameConfig{
			GameId: "liars-dice", SchemaVersion: 1, MessageType: "session.config", Payload: []byte("configured"),
		},
		OperationId: applicationOperationID(t), RequestDigest: bytes.Repeat([]byte{2}, 32),
	})
	runtime.authorizeUserWrite(t, startRequest)
	if _, err := client.StartGame(ctx, startRequest); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("start registered game with unavailable realtime runtime: %v", err)
	}
	setRequest := connect.NewRequest(&roomv1.SetAdmissionRequest{
		RoomId: created.Msg.GetRoom().GetRoomId(), ParticipantAdmission: roomv1.AdmissionMode_ADMISSION_MODE_CLOSED,
		SpectatorAdmission: roomv1.AdmissionMode_ADMISSION_MODE_OPEN, ExpectedVersion: created.Msg.GetRoom().GetVersion(),
	})
	runtime.authorizeUserWrite(t, setRequest)
	updated, err := client.SetAdmission(ctx, setRequest)
	if err != nil {
		t.Fatalf("set integrated room admission: %v", err)
	}
	if updated.Msg.GetRoom().GetParticipantAdmission() != roomv1.AdmissionMode_ADMISSION_MODE_CLOSED {
		t.Fatalf("set integrated room admission: room=%+v", updated.Msg.GetRoom())
	}
	closeRequest := connect.NewRequest(&roomv1.CloseRoomRequest{
		RoomId: updated.Msg.GetRoom().GetRoomId(), ExpectedVersion: updated.Msg.GetRoom().GetVersion(),
	})
	runtime.authorizeUserWrite(t, closeRequest)
	closed, err := client.CloseRoom(ctx, closeRequest)
	if err != nil {
		t.Fatalf("close integrated room: %v", err)
	}
	if closed.Msg.GetRoom().GetStatus() != roomv1.RoomStatus_ROOM_STATUS_CLOSED {
		t.Fatalf("close integrated room: room=%+v", closed.Msg.GetRoom())
	}
}

type integratedIdentity struct {
	userID              string
	initialCredentialID string
	currentCredentialID string
}

// onboardAndRecoverIdentity covers a fresh browser, secret receipt, recovery, and cookie replacement.
func onboardAndRecoverIdentity(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client identityv1connect.IdentityServiceClient,
) integratedIdentity {
	t.Helper()
	flowID := "bootstrap-" + uuid.NewString()
	beginRequest := connect.NewRequest(&identityv1.BeginIdentityBootstrapRequest{RequestFlowId: flowID})
	runtime.setOrigin(beginRequest, applicationUserOrigin)
	begin, err := client.BeginIdentityBootstrap(ctx, beginRequest)
	if err != nil || begin.Msg.GetChallenge().GetChallengeProof() == "" {
		t.Fatalf("begin identity bootstrap: err=%v", err)
	}
	bootstrapOperation := applicationOperationID(t)
	bootstrapRequest := connect.NewRequest(&identityv1.BootstrapIdentityRequest{
		ChallengeProof: begin.Msg.GetChallenge().GetChallengeProof(), OperationId: bootstrapOperation, DeviceLabel: "First Phone",
	})
	runtime.setOrigin(bootstrapRequest, applicationUserOrigin)
	bootstrapRequest.Header().Set(identitytransport.RequestFlowIDHeader, flowID)
	bootstrap, err := client.BootstrapIdentity(ctx, bootstrapRequest)
	if err != nil || bootstrap.Msg.GetUser().GetUserId() == "" || bootstrap.Msg.GetDevice().GetCredentialId() == "" {
		t.Fatalf("bootstrap identity: err=%v", err)
	}
	assertNoStore(t, bootstrap.Header())

	onboardingOperation := applicationOperationID(t)
	onboardingRequest := connect.NewRequest(&identityv1.CompleteOnboardingRequest{Username: "CU09", OperationId: onboardingOperation})
	runtime.authorizeUserWrite(t, onboardingRequest)
	onboarding, err := client.CompleteOnboarding(ctx, onboardingRequest)
	if err != nil || onboarding.Msg.GetUser().GetStatus() != identityv1.UserStatus_USER_STATUS_ACTIVE || onboarding.Msg.GetRecoveryCode() == "" {
		t.Fatalf("complete identity onboarding: status=%s err=%v", onboarding.Msg.GetUser().GetStatus(), err)
	}
	confirmUserSecret(t, ctx, runtime, client, identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_BOOTSTRAP, bootstrap.Msg.GetResult())
	confirmUserSecret(t, ctx, runtime, client, identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_ONBOARDING, onboarding.Msg.GetResult())

	recoveryFlowID := "recovery-" + uuid.NewString()
	recoveryChallengeRequest := connect.NewRequest(&identityv1.BeginRecoveryChallengeRequest{RequestFlowId: recoveryFlowID})
	runtime.setOrigin(recoveryChallengeRequest, applicationUserOrigin)
	recoveryChallenge, err := client.BeginRecoveryChallenge(ctx, recoveryChallengeRequest)
	if err != nil {
		t.Fatalf("begin recovery challenge: %v", err)
	}
	beginRecoveryRequest := connect.NewRequest(&identityv1.BeginRecoveryRequest{
		ChallengeProof: recoveryChallenge.Msg.GetChallenge().GetChallengeProof(), RecoveryCode: onboarding.Msg.GetRecoveryCode(),
	})
	runtime.setOrigin(beginRecoveryRequest, applicationUserOrigin)
	beginRecoveryRequest.Header().Set(identitytransport.RequestFlowIDHeader, recoveryFlowID)
	beginRecovery, err := client.BeginRecovery(ctx, beginRecoveryRequest)
	if err != nil || beginRecovery.Msg.GetRecoveryGrant() == "" {
		t.Fatalf("begin identity recovery: err=%v", err)
	}
	recoveryRequest := connect.NewRequest(&identityv1.CompleteRecoveryRequest{
		RecoveryGrant: beginRecovery.Msg.GetRecoveryGrant(), OperationId: applicationOperationID(t), DeviceLabel: "Recovered Phone",
		DevicePolicy: identityv1.RecoveryDevicePolicy_RECOVERY_DEVICE_POLICY_KEEP_OTHER_DEVICES,
	})
	runtime.setOrigin(recoveryRequest, applicationUserOrigin)
	recoveryRequest.Header().Set(identitytransport.RequestIDHeader, "request-"+uuid.NewString())
	recovery, err := client.CompleteRecovery(ctx, recoveryRequest)
	if err != nil || recovery.Msg.GetUser().GetUserId() != bootstrap.Msg.GetUser().GetUserId() || recovery.Msg.GetRecoveryCode() == "" {
		t.Fatalf("complete identity recovery: err=%v", err)
	}
	confirmUserSecret(t, ctx, runtime, client, identityv1.IdentitySecretOperation_IDENTITY_SECRET_OPERATION_RECOVERY, recovery.Msg.GetResult())
	return integratedIdentity{
		userID: bootstrap.Msg.GetUser().GetUserId(), initialCredentialID: bootstrap.Msg.GetDevice().GetCredentialId(),
		currentCredentialID: recovery.Msg.GetDevice().GetCredentialId(),
	}
}

// confirmUserSecret proves result envelopes are erased only by the current device plus CSRF authority.
func confirmUserSecret(
	t testing.TB,
	ctx context.Context,
	runtime *applicationIntegrationRuntime,
	client identityv1connect.IdentityServiceClient,
	operation identityv1.IdentitySecretOperation,
	result *commonv1.OperationResult,
) {
	t.Helper()
	request := connect.NewRequest(&identityv1.ConfirmSecretReceiptRequest{
		Operation: operation, OperationId: result.GetOperationId(), ResultId: result.GetResultId(),
	})
	runtime.authorizeUserWrite(t, request)
	response, err := client.ConfirmSecretReceipt(ctx, request)
	if err != nil || !response.Msg.GetConfirmed() {
		t.Fatalf("confirm user secret receipt: confirmed=%t err=%v", response.Msg.GetConfirmed(), err)
	}
}

type applicationIntegrationRuntime struct {
	// application owns the real PostgreSQL, Redis, Argon2, handler, and shutdown lifecycle used by the test client.
	application *Application
	// client retains both the generated TLS trust root and browser Cookie Jar across user calls.
	client *http.Client
	// serverURL is the parsed Cookie origin used to retrieve double-submit values from the Jar.
	serverURL *url.URL
	// baseURL is passed unchanged to every generated Connect client.
	baseURL string
	// serveErrors proves graceful shutdown normalized the runtime's terminal result.
	serveErrors chan error
	// redisPrefix isolates rate-limit keys so cleanup never scans or deletes another test run's namespace.
	redisPrefix string
	// logs captures structured output for the final secret and PII disclosure assertion.
	logs *bytes.Buffer
}

// newApplicationIntegrationRuntime starts the production dependency graph used by the browser-style TLS integration client.
func newApplicationIntegrationRuntime(t testing.TB) *applicationIntegrationRuntime {
	t.Helper()
	values := integrationtest.RequireEnvironment(t, integrationtest.DependencyPostgres, applicationTestDatabaseEnvironment)
	redisValues := integrationtest.RequireEnvironment(t, integrationtest.DependencyRedis, applicationTestRedisEnvironment)
	fixture := integrationtest.OpenPostgresSchema(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	applyApplicationIntegrationMigrations(t, ctx, fixture)
	keyrings := applicationIntegrationKeyrings(t, time.Now().UTC())
	bootstrapFile := writeApplicationReadOnlyFile(t, "admin-bootstrap.txt", []byte(applicationBootstrapPassword+"\n"))
	if secret, mounted, err := bootstrap.ReadSecret(bootstrapFile); err != nil || !mounted || secret != applicationBootstrapPassword {
		t.Fatalf("read integrated administrator bootstrap secret: mounted=%t err=%v", mounted, err)
	}
	redisPrefix := "gn:e2e:" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16] + ":"
	realtimeServer := newDisabledRealtimeServer(t)
	config := apiConfig.Config{
		Shared: sharedconfig.Config{
			Environment: sharedconfig.EnvironmentTest,
			PostgreSQL: sharedconfig.PostgreSQLConfig{
				DSN: values[0], Schema: fixture.Name, MinConnections: 1, MaxConnections: 4,
				MaxConnectionLifetime: time.Hour, MaxConnectionIdleTime: 5 * time.Minute, HealthCheckPeriod: time.Minute,
			},
			Redis: sharedconfig.RedisConfig{URL: redisValues[0], Timeout: 2 * time.Second, KeyPrefix: redisPrefix},
			Network: sharedconfig.NetworkConfig{
				UserOrigins:    sharedconfig.OriginAllowlist{sharedconfig.Origin(applicationUserOrigin)},
				AdminOrigins:   sharedconfig.OriginAllowlist{sharedconfig.Origin(applicationAdminOrigin)},
				TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, CookieSecure: true,
			},
			Checkpoint: sharedconfig.CheckpointConfig{MaxEvents: 100, MaxInterval: 5 * time.Minute},
			Keyrings:   keyrings, BootstrapSecretFile: sharedconfig.BootstrapSecretFile(bootstrapFile),
		},
		Listener: apiConfig.ListenerConfig{
			Address: "127.0.0.1:8080", ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, ShutdownTimeout: 5 * time.Second, MaxHeaderBytes: 1 << 20,
		},
		Argon2: apiConfig.Argon2Config{Workers: 1, QueueCapacity: 16},
		Realtime: apiConfig.RealtimeConfig{
			BootstrapURL: realtimeServer.URL, PeerURLs: []string{realtimeServer.URL}, InternalToken: strings.Repeat("r", 32),
		},
		InstanceID: "api-integration",
		Heartbeat: serviceheartbeat.Config{
			Token: strings.Repeat("h", 32), BuildVersion: "integration", Interval: serviceheartbeat.DefaultInterval, Timeout: serviceheartbeat.DefaultTimeout,
		},
	}
	logs := &bytes.Buffer{}
	registry, err := gameregistry.New()
	if err != nil {
		t.Fatalf("build integrated game registry: %v", err)
	}
	application, err := New(ctx, config, Options{
		Logger: slog.New(logging.NewJSONHandler(logs, slog.LevelDebug)), Metrics: prometheus.NewRegistry(),
		CheckpointSink: audit.SinkReadinessFunc(func(context.Context) bool { return true }),
		Registry:       registry,
	})
	if err != nil {
		var status string
		var passwordPresent bool
		if queryErr := fixture.Pool.QueryRow(ctx, `
			SELECT status, password_hash IS NOT NULL FROM admin_accounts WHERE singleton_id = 1
		`).Scan(&status, &passwordPresent); queryErr != nil {
			t.Fatalf("build integrated API application: %v; inspect administrator state: %v", err, queryErr)
		}
		t.Fatalf("build integrated API application: %v; administrator status=%s password_present=%t", err, status, passwordPresent)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = application.closeDependencies()
		t.Fatal(err)
	}
	certificate, roots := applicationIntegrationTLSIdentity(t)
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- application.runtime.Serve(tlsListener) }()
	baseURL := "https://" + listener.Addr().String()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12,
	}}}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	serverURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &applicationIntegrationRuntime{
		application: application, client: client, serverURL: serverURL, baseURL: baseURL,
		serveErrors: serveErrors, redisPrefix: redisPrefix, logs: logs,
	}
	t.Cleanup(func() {
		cleanupApplicationIntegrationRedis(t, application, redisPrefix)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := application.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown integrated API application: %v", err)
		}
		if err := <-serveErrors; err != nil {
			t.Errorf("serve integrated API application: %v", err)
		}
	})
	return runtime
}

type disabledRealtimeService struct {
	realtimev1connect.UnimplementedOwnerServiceHandler
}

func (*disabledRealtimeService) StartSession(
	context.Context,
	*connect.Request[realtimev1.StartSessionRequest],
) (*connect.Response[realtimev1.StartSessionResponse], error) {
	connectError := connect.NewError(connect.CodeUnavailable, errors.New("game.module.unavailable"))
	detail, err := connect.NewErrorDetail(&commonv1.BusinessErrorDetail{
		Code:       commonv1.BusinessErrorCode_BUSINESS_ERROR_CODE_GAME_MODULE_UNAVAILABLE,
		MessageKey: "game.module.unavailable",
	})
	if err != nil {
		return nil, err
	}
	connectError.AddDetail(detail)
	return nil, connectError
}

func newDisabledRealtimeServer(t testing.TB) *httptest.Server {
	t.Helper()
	path, handler := realtimev1connect.NewOwnerServiceHandler(&disabledRealtimeService{})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// authorizeUserWrite applies the exact browser Origin and double-submit token expected by user mutations.
func (runtime *applicationIntegrationRuntime) authorizeUserWrite(t testing.TB, request interface{ Header() http.Header }) {
	t.Helper()
	runtime.setOriginHeader(request.Header(), applicationUserOrigin)
	request.Header().Set(csrf.HeaderName, runtime.cookie(t, cookies.UserCSRFCookieName))
}

func (runtime *applicationIntegrationRuntime) setOrigin(request interface{ Header() http.Header }, origin string) {
	runtime.setOriginHeader(request.Header(), origin)
}

func (runtime *applicationIntegrationRuntime) setOriginHeader(header http.Header, origin string) {
	header.Set("Origin", origin)
}

func (runtime *applicationIntegrationRuntime) cookie(t testing.TB, name string) string {
	t.Helper()
	for _, cookie := range runtime.client.Jar.Cookies(runtime.serverURL) {
		if cookie.Name == name && cookie.Value != "" {
			return cookie.Value
		}
	}
	t.Fatalf("browser Cookie %s is unavailable", name)
	return ""
}

// applyApplicationIntegrationMigrations binds all deployment roles to the isolated schema owner for this non-privilege suite.
func applyApplicationIntegrationMigrations(t testing.TB, ctx context.Context, fixture *integrationtest.PostgresSchema) {
	t.Helper()
	var currentUser string
	if err := fixture.Pool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	database := fixture.OpenSQLDB(t, map[string]string{
		"game_night.owner_role": currentUser, "game_night.audit_writer_role": currentUser,
		"game_night.migration_role": currentUser, "game_night.runtime_role": currentUser, "game_night.worker_role": currentUser,
	})
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate application integration test source")
	}
	migrations := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "..", "infra", "migrations"))
	if err := goose.UpContext(ctx, database, migrations); err != nil {
		t.Fatalf("apply application integration migrations: %v", err)
	}
}

// applicationIntegrationKeyrings creates one independently generated key per cryptographic purpose.
func applicationIntegrationKeyrings(t testing.TB, now time.Time) sharedconfig.KeyringFiles {
	t.Helper()
	symmetric := func(name string) string {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			t.Fatal(err)
		}
		document := map[string]any{
			"active_version": 1,
			"keys": []map[string]any{{
				"version": 1, "key": base64.StdEncoding.EncodeToString(key), "not_before": now.Add(-time.Hour),
			}},
		}
		contents, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return writeApplicationReadOnlyFile(t, name, contents)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auditDocument := map[string]any{
		"active_version": 1,
		"keys": []map[string]any{{
			"version": 1, "public_key": base64.StdEncoding.EncodeToString(publicKey),
			"private_key": base64.StdEncoding.EncodeToString(privateKey), "not_before": now.Add(-time.Hour),
		}},
	}
	auditContents, err := json.Marshal(auditDocument)
	if err != nil {
		t.Fatal(err)
	}
	return sharedconfig.KeyringFiles{
		PII:            sharedconfig.PIIKeyringFile(symmetric("pii.json")),
		TOTP:           sharedconfig.TOTPKeyringFile(symmetric("totp.json")),
		ResultEnvelope: sharedconfig.ResultEnvelopeKeyringFile(symmetric("result-envelope.json")),
		Device:         sharedconfig.DeviceKeyringFile(symmetric("device.json")),
		RateLimit:      sharedconfig.RateLimitKeyringFile(symmetric("rate-limit.json")),
		UserChallenge:  sharedconfig.UserChallengeKeyringFile(symmetric("user-challenge.json")),
		AdminChallenge: sharedconfig.AdminChallengeKeyringFile(symmetric("admin-challenge.json")),
		AdminSession:   sharedconfig.AdminSessionKeyringFile(symmetric("admin-session.json")),
		AdminCursor:    sharedconfig.AdminCursorKeyringFile(symmetric("admin-cursor.json")),
		Audit:          sharedconfig.AuditKeyringFile(writeApplicationReadOnlyFile(t, "audit.json", auditContents)),
	}
}

func writeApplicationReadOnlyFile(t testing.TB, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o400)
	if runtime.GOOS == "windows" {
		mode = 0o444
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}

// applicationIntegrationTLSIdentity creates a trusted loopback certificate so Secure Cookies follow browser rules.
func applicationIntegrationTLSIdentity(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("trust integrated API certificate")
	}
	return certificate, roots
}

func cleanupApplicationIntegrationRedis(t testing.TB, application *Application, prefix string) {
	t.Helper()
	if application == nil || application.redis == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cursor uint64
	for {
		keys, next, err := application.redis.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			t.Errorf("scan integrated Redis keys")
			return
		}
		if len(keys) > 0 {
			if err := application.redis.Unlink(ctx, keys...).Err(); err != nil {
				t.Errorf("delete integrated Redis keys")
				return
			}
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}

func applicationOperationID(t testing.TB) string {
	t.Helper()
	entropy := make([]byte, 16)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(entropy)
}

func assertNoStore(t testing.TB, header http.Header) {
	t.Helper()
	if header.Get("Cache-Control") != "no-store" || header.Get("Pragma") != "no-cache" {
		t.Fatalf("sensitive response cache policy = %q / %q", header.Get("Cache-Control"), header.Get("Pragma"))
	}
}
