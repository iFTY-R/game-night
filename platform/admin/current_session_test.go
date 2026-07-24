package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	adminv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/admin/v1"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identity"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/profile"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

const (
	currentSessionPasswordHash       = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	currentSessionPasswordAlgorithm  = PasswordAlgorithmArgon2id
	currentSessionPasswordParameters = `{"MemoryKiB":65536,"Iterations":3,"Parallelism":2,"SaltLength":16,"KeyLength":32}`
)

func TestGetCurrentAdminSessionReturnsExactStateMatrix(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		account     AccountStatus
		sessionKind SessionKind
		nextStep    adminv1.AdminNextStep
		wireKind    adminv1.AdminSessionKind
		permissions []adminv1.AdminPermission
	}{
		{
			name:        "setup password pending",
			account:     AccountStatusSetupRequired,
			sessionKind: SessionKindSetupPasswordPending,
			nextStep:    adminv1.AdminNextStep_ADMIN_NEXT_STEP_CHANGE_PASSWORD,
			wireKind:    adminv1.AdminSessionKind_ADMIN_SESSION_KIND_SETUP_PASSWORD_PENDING,
		},
		{
			name:        "totp enrollment pending",
			account:     AccountStatusActive,
			sessionKind: SessionKindTOTPEnrollmentPending,
			nextStep:    adminv1.AdminNextStep_ADMIN_NEXT_STEP_ENROLL_TOTP,
			wireKind:    adminv1.AdminSessionKind_ADMIN_SESSION_KIND_TOTP_ENROLLMENT_PENDING,
		},
		{
			name:        "mfa pending",
			account:     AccountStatusActive,
			sessionKind: SessionKindMFAPending,
			nextStep:    adminv1.AdminNextStep_ADMIN_NEXT_STEP_VERIFY_MFA,
			wireKind:    adminv1.AdminSessionKind_ADMIN_SESSION_KIND_MFA_PENDING,
		},
		{
			name:        "recovery pending",
			account:     AccountStatusRecoveryPending,
			sessionKind: SessionKindRecoveryPending,
			nextStep:    adminv1.AdminNextStep_ADMIN_NEXT_STEP_REBIND_TOTP,
			wireKind:    adminv1.AdminSessionKind_ADMIN_SESSION_KIND_RECOVERY_PENDING,
		},
		{
			name:        "full",
			account:     AccountStatusActive,
			sessionKind: SessionKindFull,
			nextStep:    adminv1.AdminNextStep_ADMIN_NEXT_STEP_AUTHENTICATED,
			wireKind:    adminv1.AdminSessionKind_ADMIN_SESSION_KIND_FULL,
			permissions: []adminv1.AdminPermission{
				adminv1.AdminPermission_ADMIN_PERMISSION_GET_USER,
				adminv1.AdminPermission_ADMIN_PERMISSION_GET_REAL_NAME,
				adminv1.AdminPermission_ADMIN_PERMISSION_UPDATE_REAL_NAME,
				adminv1.AdminPermission_ADMIN_PERMISSION_EXPORT_PROFILE,
				adminv1.AdminPermission_ADMIN_PERMISSION_MANAGE_RECOVERY,
				adminv1.AdminPermission_ADMIN_PERMISSION_FORCE_USERNAME,
				adminv1.AdminPermission_ADMIN_PERMISSION_SUSPEND_USER,
				adminv1.AdminPermission_ADMIN_PERMISSION_DELETE_USER,
				adminv1.AdminPermission_ADMIN_PERMISSION_REVOKE_DEVICE,
				adminv1.AdminPermission_ADMIN_PERMISSION_READ_AUDIT,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newCurrentSessionFixture(t, now, test.account, test.sessionKind)
			response, err := fixture.callCurrentSession(fixture.issued.Token, fixture.issued.CSRFToken)
			if err != nil {
				t.Fatalf("GetCurrentAdminSession error = %v", err)
			}
			if got := response.Msg.GetNextStep(); got != test.nextStep {
				t.Fatalf("next step = %s, want %s", got, test.nextStep)
			}
			session := response.Msg.GetSession()
			if session.GetAdminId() != fixture.account.Snapshot().ID.String() {
				t.Fatalf("admin id = %s, want %s", session.GetAdminId(), fixture.account.Snapshot().ID)
			}
			if session.GetKind() != test.wireKind {
				t.Fatalf("session kind = %s, want %s", session.GetKind(), test.wireKind)
			}
			if !session.GetIdleExpiresAt().AsTime().Equal(fixture.issued.Session.Snapshot().IdleExpiresAt) {
				t.Fatalf("idle expiry = %s, want %s", session.GetIdleExpiresAt().AsTime(), fixture.issued.Session.Snapshot().IdleExpiresAt)
			}
			if !session.GetAbsoluteExpiresAt().AsTime().Equal(fixture.issued.Session.Snapshot().AbsoluteExpiresAt) {
				t.Fatalf("absolute expiry = %s, want %s", session.GetAbsoluteExpiresAt().AsTime(), fixture.issued.Session.Snapshot().AbsoluteExpiresAt)
			}
			if !slices.Equal(session.GetPermissions(), test.permissions) {
				t.Fatalf("permissions = %v, want %v", session.GetPermissions(), test.permissions)
			}
			if fixture.sessions.getForUpdateCalls != 1 || fixture.accounts.getForUpdateCalls != 1 {
				t.Fatalf("unexpected repository calls: sessions=%d accounts=%d", fixture.sessions.getForUpdateCalls, fixture.accounts.getForUpdateCalls)
			}
			if fixture.sessions.touchCalls != 0 {
				t.Fatalf("TouchCAS must stay unused, got %d", fixture.sessions.touchCalls)
			}
			if fixture.sessionWrites() != 0 {
				t.Fatalf("unexpected session side effects: %d", fixture.sessionWrites())
			}
			if fixture.secretResults.insertCalls != 0 {
				t.Fatalf("unexpected secret result writes: %d", fixture.secretResults.insertCalls)
			}
		})
	}
}

func TestGetCurrentAdminSessionRejectsInvalidProofsWithoutTouchingState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 11, 0, 0, 0, time.UTC)
	base := newCurrentSessionFixture(t, now, AccountStatusActive, SessionKindFull)

	tests := []struct {
		name             string
		mutate           func(*currentSessionFixture, string, string) (string, string)
		wantSessionLoads int
		wantAccountLoads int
	}{
		{
			name: "invalid selector format",
			mutate: func(_ *currentSessionFixture, _, csrf string) (string, string) {
				return "not-a-valid-token", csrf
			},
		},
		{
			name: "unknown selector",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				fixture.sessions.forceNotFound = true
				return token, csrf
			},
			wantSessionLoads: 1,
		},
		{
			name: "token hmac mismatch",
			mutate: func(fixture *currentSessionFixture, _ string, csrf string) (string, string) {
				return fixture.issued.CSRFToken, csrf
			},
			wantSessionLoads: 1,
		},
		{
			name: "csrf mismatch",
			mutate: func(_ *currentSessionFixture, token, _ string) (string, string) {
				return token, base.issued.Token
			},
			wantSessionLoads: 1,
		},
		{
			name: "revoked",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				revoked, err := fixture.issued.Session.Revoke("manual", fixture.now.Add(time.Minute))
				if err != nil {
					t.Fatal(err)
				}
				fixture.issued.Session = revoked
				fixture.sessions.session = revoked
				return token, csrf
			},
			wantSessionLoads: 1,
		},
		{
			name: "idle expired",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				expired := fixture.issued.Session.Snapshot()
				expired.CreatedAt = fixture.now.Add(-5 * time.Minute)
				expired.LastSeenAt = fixture.now.Add(-2 * time.Second)
				expired.IdleExpiresAt = fixture.now
				fixture.issued.Session = mustRestoreSession(t, expired)
				fixture.sessions.session = fixture.issued.Session
				return token, csrf
			},
			wantSessionLoads: 1,
		},
		{
			name: "absolute expired",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				expired := fixture.issued.Session.Snapshot()
				expired.CreatedAt = fixture.now.Add(-5 * time.Minute)
				expired.LastSeenAt = fixture.now.Add(-2 * time.Second)
				expired.IdleExpiresAt = fixture.now.Add(-time.Second)
				expired.AbsoluteExpiresAt = fixture.now
				fixture.issued.Session = mustRestoreSession(t, expired)
				fixture.sessions.session = fixture.issued.Session
				return token, csrf
			},
			wantSessionLoads: 1,
		},
		{
			name: "admin version changed",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				account := fixture.account.Snapshot()
				account.AdminVersion++
				fixture.account = mustRestoreAccount(t, account)
				fixture.accounts.account = fixture.account
				return token, csrf
			},
			wantSessionLoads: 1,
			wantAccountLoads: 1,
		},
		{
			name: "password version changed",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				account := fixture.account.Snapshot()
				account.PasswordVersion++
				fixture.account = mustRestoreAccount(t, account)
				fixture.accounts.account = fixture.account
				return token, csrf
			},
			wantSessionLoads: 1,
			wantAccountLoads: 1,
		},
		{
			name: "account missing",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				fixture.accounts.err = ErrNotFound
				return token, csrf
			},
			wantSessionLoads: 1,
			wantAccountLoads: 1,
		},
		{
			name: "account unavailable",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				fixture.accounts.err = ErrRepositoryUnavailable
				return token, csrf
			},
			wantSessionLoads: 1,
			wantAccountLoads: 1,
		},
		{
			name: "unknown session kind",
			mutate: func(fixture *currentSessionFixture, token, csrf string) (string, string) {
				snapshot := fixture.issued.Session.Snapshot()
				snapshot.Kind = SessionKind("broken")
				fixture.issued.Session = Session{snapshot: snapshot}
				fixture.sessions.session = fixture.issued.Session
				return token, csrf
			},
			wantSessionLoads: 1,
			wantAccountLoads: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newCurrentSessionFixture(t, now, AccountStatusActive, SessionKindFull)
			token, csrfToken := test.mutate(fixture, fixture.issued.Token, fixture.issued.CSRFToken)
			_, err := fixture.callCurrentSession(token, csrfToken)
			if code := connect.CodeOf(err); code != connect.CodeUnauthenticated && code != connect.CodeFailedPrecondition {
				t.Fatalf("error = %v", err)
			}
			if test.wantAccountLoads > 0 && !errors.Is(err, ErrAuthentication) {
				t.Fatalf("expected normalized authentication error, got %v", err)
			}
			if fixture.sessions.getForUpdateCalls != test.wantSessionLoads {
				t.Fatalf("session loads = %d, want %d", fixture.sessions.getForUpdateCalls, test.wantSessionLoads)
			}
			if fixture.accounts.getForUpdateCalls != test.wantAccountLoads {
				t.Fatalf("account loads = %d, want %d", fixture.accounts.getForUpdateCalls, test.wantAccountLoads)
			}
			if fixture.sessions.touchCalls != 0 || fixture.sessionWrites() != 0 || fixture.secretResults.insertCalls != 0 {
				t.Fatalf("unexpected side effects: touch=%d writes=%d resultWrites=%d", fixture.sessions.touchCalls, fixture.sessionWrites(), fixture.secretResults.insertCalls)
			}
		})
	}
}

func TestBeginTotpEnrollmentRecoversPendingOperationServerSide(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	fixture := newEnrollmentFixture(t, now, AccountStatusActive, SessionKindTOTPEnrollmentPending)
	firstOperation := newOperationID(t, 0x11)
	secondOperation := newOperationID(t, 0x22)

	first, err := fixture.service.BeginTotpEnrollment(t.Context(), BeginEnrollmentCommand{
		Session: fixture.issued.Session, SessionToken: fixture.issued.Token, CSRFToken: fixture.issued.CSRFToken, OperationID: firstOperation,
	})
	if err != nil {
		t.Fatalf("first begin error = %v", err)
	}
	if first.Operation.OperationID != firstOperation || first.Operation.Replayed {
		t.Fatalf("first operation = %+v", first.Operation)
	}
	if fixture.enrollments.createPendingCalls != 1 || fixture.secretResults.insertCalls != 1 {
		t.Fatalf("initial writes = enrollments:%d results:%d", fixture.enrollments.createPendingCalls, fixture.secretResults.insertCalls)
	}

	repeated, err := fixture.service.BeginTotpEnrollment(t.Context(), BeginEnrollmentCommand{
		Session: fixture.issued.Session, SessionToken: fixture.issued.Token, CSRFToken: fixture.issued.CSRFToken, OperationID: firstOperation,
	})
	if err != nil {
		t.Fatalf("repeat begin error = %v", err)
	}
	if repeated.Operation.OperationID != firstOperation || !repeated.Operation.Replayed || repeated.Secret != first.Secret || repeated.URI != first.URI {
		t.Fatalf("repeat recovery = %+v", repeated.Operation)
	}

	recovered, err := fixture.service.BeginTotpEnrollment(t.Context(), BeginEnrollmentCommand{
		Session: fixture.issued.Session, SessionToken: fixture.issued.Token, CSRFToken: fixture.issued.CSRFToken, OperationID: secondOperation,
	})
	if err != nil {
		t.Fatalf("recovery begin error = %v", err)
	}
	if recovered.Operation.OperationID != firstOperation || !recovered.Operation.Replayed || recovered.Secret != first.Secret || recovered.URI != first.URI {
		t.Fatalf("server-side recovery = %+v", recovered.Operation)
	}
	if fixture.enrollments.createPendingCalls != 1 || fixture.secretResults.insertCalls != 1 {
		t.Fatalf("recovery created new secret material: enrollments=%d results=%d", fixture.enrollments.createPendingCalls, fixture.secretResults.insertCalls)
	}
}

func TestBeginTotpEnrollmentRefusesUnavailableRecoveredSecret(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 13, 0, 0, 0, time.UTC)
	fixture := newEnrollmentFixture(t, now, AccountStatusActive, SessionKindTOTPEnrollmentPending)
	firstOperation := newOperationID(t, 0x31)
	_, err := fixture.service.BeginTotpEnrollment(t.Context(), BeginEnrollmentCommand{
		Session: fixture.issued.Session, SessionToken: fixture.issued.Token, CSRFToken: fixture.issued.CSRFToken, OperationID: firstOperation,
	})
	if err != nil {
		t.Fatalf("first begin error = %v", err)
	}

	key := adminResultBinding(
		secretresult.ScopeAdminTOTPEnrollment,
		fixture.account.Snapshot().ID,
		firstOperation,
		digestAdminRequest("admin.totp_enrollment", string(SessionKindTOTPEnrollmentPending)),
		secretresult.ResultTypeAdminTOTPEnrollment,
	).Key
	stored, err := fixture.secretResults.GetByOperationForUpdate(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := stored.Confirm(now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	fixture.secretResults.results[key] = confirmed

	_, err = fixture.service.BeginTotpEnrollment(t.Context(), BeginEnrollmentCommand{
		Session: fixture.issued.Session, SessionToken: fixture.issued.Token, CSRFToken: fixture.issued.CSRFToken, OperationID: newOperationID(t, 0x32),
	})
	if !errors.Is(err, secretresult.ErrSecretNoLongerAvailable) {
		t.Fatalf("recovery error = %v", err)
	}
	if fixture.enrollments.createPendingCalls != 1 || fixture.secretResults.insertCalls != 1 {
		t.Fatalf("confirmed secret triggered new writes: enrollments=%d results=%d", fixture.enrollments.createPendingCalls, fixture.secretResults.insertCalls)
	}
}

func TestBeginTotpRebindUsesIndependentRecoveryScope(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	fixture := newEnrollmentFixture(t, now, AccountStatusRecoveryPending, SessionKindRecoveryPending)
	adapter, err := NewConnectAdminService(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	firstOperation := newOperationID(t, 0x41)
	secondOperation := newOperationID(t, 0x42)

	first, err := adapter.BeginTotpRebind(
		WithAdminTransportContext(t.Context(), AdminTransportContext{CookieToken: fixture.issued.Token, CSRFToken: fixture.issued.CSRFToken}),
		connect.NewRequest(&adminv1.BeginTotpRebindRequest{OperationId: firstOperation.Value()}),
	)
	if err != nil {
		t.Fatalf("first rebind error = %v", err)
	}
	recovered, err := adapter.BeginTotpRebind(
		WithAdminTransportContext(t.Context(), AdminTransportContext{CookieToken: fixture.issued.Token, CSRFToken: fixture.issued.CSRFToken}),
		connect.NewRequest(&adminv1.BeginTotpRebindRequest{OperationId: secondOperation.Value()}),
	)
	if err != nil {
		t.Fatalf("recovered rebind error = %v", err)
	}
	if recovered.Msg.GetResult().GetOperationId() != first.Msg.GetResult().GetOperationId() || !recovered.Msg.GetResult().GetReplayed() {
		t.Fatalf("rebind recovery = %+v", recovered.Msg.GetResult())
	}
}

type currentSessionFixture struct {
	now           time.Time
	service       *Service
	adapter       *ConnectAdminService
	account       Account
	issued        IssuedSession
	sessionKeys   *security.HMACKeyring[security.AdminSessionKeyPurpose]
	accounts      *trackingAccountRepository
	sessions      *trackingSessionRepository
	enrollments   *trackingEnrollmentRepository
	secretResults *trackingSecretResultRepository
}

func newCurrentSessionFixture(t testing.TB, now time.Time, accountStatus AccountStatus, sessionKind SessionKind) *currentSessionFixture {
	t.Helper()

	account := restoreTestAccount(t, accountStatus, now)
	sessionKeyring := loadAdminSessionHMACKeyring(t, now)
	sessionService, err := NewSessionService(sessionKeyring, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := sessionService.Issue(account.Snapshot().ID, sessionKind, account.Snapshot().AdminVersion, account.Snapshot().PasswordVersion, now)
	if err != nil {
		t.Fatal(err)
	}
	accounts := &trackingAccountRepository{account: account}
	sessions := &trackingSessionRepository{session: issued.Session}
	enrollments := &trackingEnrollmentRepository{}
	secretResults := newTrackingSecretResultRepository()
	service := &Service{
		sessions: sessionService,
		clock:    clock.NewFake(now),
		unitOfWork: trackingUnitOfWork{
			accounts:      accounts,
			enrollments:   enrollments,
			sessions:      sessions,
			secretResults: secretResults,
		},
	}
	adapter, err := NewConnectAdminService(service)
	if err != nil {
		t.Fatal(err)
	}
	return &currentSessionFixture{
		now:           now,
		service:       service,
		adapter:       adapter,
		account:       account,
		issued:        issued,
		sessionKeys:   sessionKeyring,
		accounts:      accounts,
		sessions:      sessions,
		enrollments:   enrollments,
		secretResults: secretResults,
	}
}

func newEnrollmentFixture(t testing.TB, now time.Time, accountStatus AccountStatus, sessionKind SessionKind) *currentSessionFixture {
	t.Helper()

	fixture := newCurrentSessionFixture(t, now, accountStatus, sessionKind)
	totpService, err := NewTOTPService(loadAdminTOTPKeyring(t, now))
	if err != nil {
		t.Fatal(err)
	}
	resultService, err := secretresult.NewServiceWithAdminAccess(
		testEnvelopeCipher(t, now),
		clock.NewFake(now),
		fixture.sessionKeys,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.totp = totpService
	fixture.service.results = resultService
	return fixture
}

func (fixture *currentSessionFixture) callCurrentSession(token, csrfToken string) (*connect.Response[adminv1.GetCurrentAdminSessionResponse], error) {
	return fixture.adapter.GetCurrentAdminSession(
		WithAdminTransportContext(context.Background(), AdminTransportContext{CookieToken: token, CSRFToken: csrfToken}),
		connect.NewRequest(&adminv1.GetCurrentAdminSessionRequest{}),
	)
}

func (fixture *currentSessionFixture) sessionWrites() int {
	return fixture.sessions.insertCalls + fixture.sessions.revokeCalls + fixture.sessions.revokeAllCalls
}

type trackingUnitOfWork struct {
	accounts      *trackingAccountRepository
	enrollments   *trackingEnrollmentRepository
	sessions      *trackingSessionRepository
	secretResults *trackingSecretResultRepository
}

func (unitOfWork trackingUnitOfWork) Run(ctx context.Context, work TransactionWork) error {
	return work(ctx, trackingTransaction{
		accounts:      unitOfWork.accounts,
		enrollments:   unitOfWork.enrollments,
		sessions:      unitOfWork.sessions,
		secretResults: unitOfWork.secretResults,
	})
}

type trackingTransaction struct {
	accounts      *trackingAccountRepository
	enrollments   *trackingEnrollmentRepository
	sessions      *trackingSessionRepository
	secretResults *trackingSecretResultRepository
}

func (transaction trackingTransaction) Challenges() ChallengeRepository { return nil }
func (transaction trackingTransaction) SecretResults() secretresult.Repository {
	return transaction.secretResults
}
func (transaction trackingTransaction) Accounts() AccountRepository { return transaction.accounts }
func (transaction trackingTransaction) Enrollments() EnrollmentRepository {
	return transaction.enrollments
}
func (transaction trackingTransaction) Sessions() SessionRepository           { return transaction.sessions }
func (transaction trackingTransaction) RecoveryCodes() RecoveryCodeRepository { return nil }
func (transaction trackingTransaction) IdentityUsers() IdentityUserRepository { return nil }
func (transaction trackingTransaction) IdentityUsernameClaims() identity.UsernameClaimRepository {
	return nil
}
func (transaction trackingTransaction) IdentityDevices() identity.DeviceRepository { return nil }
func (transaction trackingTransaction) IdentityRecoveryCredentials() identity.RecoveryCredentialRepository {
	return nil
}
func (transaction trackingTransaction) Profiles() profile.Repository             { return nil }
func (transaction trackingTransaction) ProfileExports() profile.ExportRepository { return nil }
func (transaction trackingTransaction) AssistedRecoveryGrants() AssistedRecoveryGrantRepository {
	return nil
}
func (transaction trackingTransaction) Audit() audit.Repository                      { return nil }
func (transaction trackingTransaction) AuditCheckpoints() audit.CheckpointRepository { return nil }
func (transaction trackingTransaction) OutboxEvents() outbox.EventRepository         { return nil }

type trackingAccountRepository struct {
	account           Account
	err               error
	getForUpdateCalls int
}

func (repository *trackingAccountRepository) GetForUpdate(context.Context) (Account, error) {
	repository.getForUpdateCalls++
	if repository.err != nil {
		return Account{}, repository.err
	}
	return repository.account, nil
}

func (*trackingAccountRepository) BootstrapPasswordCAS(context.Context, Account, string, string, string, time.Time) (Account, error) {
	return Account{}, ErrConcurrentTransition
}
func (*trackingAccountRepository) UpdatePasswordCAS(context.Context, Account, string, string, string, time.Time) (Account, error) {
	return Account{}, ErrConcurrentTransition
}
func (*trackingAccountRepository) TransitionStatusCAS(context.Context, Account, AccountStatus, time.Time) (Account, error) {
	return Account{}, ErrConcurrentTransition
}
func (*trackingAccountRepository) AcceptTOTPStepCAS(context.Context, Account, int64, time.Time) (Account, error) {
	return Account{}, ErrConcurrentTransition
}

type trackingSessionRepository struct {
	session           Session
	forceNotFound     bool
	getForUpdateCalls int
	insertCalls       int
	touchCalls        int
	revokeCalls       int
	revokeAllCalls    int
}

func (repository *trackingSessionRepository) Insert(context.Context, Session) error {
	repository.insertCalls++
	return nil
}

func (repository *trackingSessionRepository) GetForUpdate(_ context.Context, selector string) (Session, error) {
	repository.getForUpdateCalls++
	if repository.forceNotFound || repository.session.Snapshot().Selector != selector {
		return Session{}, ErrNotFound
	}
	return repository.session, nil
}

func (repository *trackingSessionRepository) TouchCAS(context.Context, Session, time.Time, time.Duration) (Session, error) {
	repository.touchCalls++
	return Session{}, ErrConcurrentTransition
}

func (repository *trackingSessionRepository) RevokeCAS(context.Context, Session, string, time.Time) (Session, error) {
	repository.revokeCalls++
	return Session{}, ErrConcurrentTransition
}

func (repository *trackingSessionRepository) RevokeAll(context.Context, uuid.UUID, string, time.Time) (int64, error) {
	repository.revokeAllCalls++
	return 0, ErrConcurrentTransition
}

type trackingEnrollmentRepository struct {
	pending            Enrollment
	active             Enrollment
	createPendingCalls int
	getPendingCalls    int
	getActiveCalls     int
}

func (repository *trackingEnrollmentRepository) CreatePending(_ context.Context, enrollment Enrollment) (Enrollment, error) {
	repository.createPendingCalls++
	if repository.pending.Snapshot().ID != uuid.Nil {
		return Enrollment{}, ErrConcurrentTransition
	}
	repository.pending = enrollment
	return enrollment, nil
}

func (repository *trackingEnrollmentRepository) GetPendingForUpdate(context.Context, uuid.UUID) (Enrollment, error) {
	repository.getPendingCalls++
	if repository.pending.Snapshot().ID == uuid.Nil {
		return Enrollment{}, ErrNotFound
	}
	return repository.pending, nil
}

func (repository *trackingEnrollmentRepository) GetActiveForUpdate(context.Context, uuid.UUID) (Enrollment, error) {
	repository.getActiveCalls++
	if repository.active.Snapshot().ID == uuid.Nil {
		return Enrollment{}, ErrNotFound
	}
	return repository.active, nil
}

func (repository *trackingEnrollmentRepository) ActivateCAS(context.Context, Enrollment, time.Time) (Enrollment, error) {
	return Enrollment{}, ErrConcurrentTransition
}

func (repository *trackingEnrollmentRepository) DisableCAS(context.Context, Enrollment, time.Time) (Enrollment, error) {
	return Enrollment{}, ErrConcurrentTransition
}

type trackingSecretResultRepository struct {
	results     map[secretresult.Key]secretresult.Result
	insertCalls int
}

func newTrackingSecretResultRepository() *trackingSecretResultRepository {
	return &trackingSecretResultRepository{results: make(map[secretresult.Key]secretresult.Result)}
}

func (repository *trackingSecretResultRepository) InsertAvailable(_ context.Context, result secretresult.Result) (secretresult.Result, error) {
	repository.insertCalls++
	key := result.Snapshot().Binding.Key
	if existing, exists := repository.results[key]; exists {
		return existing, nil
	}
	repository.results[key] = result
	return result, nil
}

func (repository *trackingSecretResultRepository) GetByOperationForUpdate(_ context.Context, key secretresult.Key) (secretresult.Result, error) {
	result, exists := repository.results[key]
	if !exists {
		return secretresult.Result{}, secretresult.ErrNotFound
	}
	return result, nil
}

func (repository *trackingSecretResultRepository) GetByIDForUpdate(_ context.Context, resultID uuid.UUID) (secretresult.Result, error) {
	for _, result := range repository.results {
		if result.Snapshot().ID == resultID {
			return result, nil
		}
	}
	return secretresult.Result{}, secretresult.ErrNotFound
}

func (repository *trackingSecretResultRepository) ConfirmCAS(_ context.Context, confirmation secretresult.Confirmation) (secretresult.Result, error) {
	current, exists := repository.results[confirmation.Binding.Key]
	if !exists || current.Snapshot().ID != confirmation.ResultID {
		return secretresult.Result{}, secretresult.ErrConcurrentTransition
	}
	confirmed, err := current.Confirm(confirmation.ConfirmedAt)
	if err != nil {
		return secretresult.Result{}, err
	}
	repository.results[confirmation.Binding.Key] = confirmed
	return confirmed, nil
}

func (repository *trackingSecretResultRepository) ExpireCAS(_ context.Context, result secretresult.Result, expiredAt time.Time) (secretresult.Result, error) {
	key := result.Snapshot().Binding.Key
	current, exists := repository.results[key]
	if !exists {
		return secretresult.Result{}, secretresult.ErrNotFound
	}
	expired, err := current.Expire(expiredAt)
	if err != nil {
		return secretresult.Result{}, err
	}
	repository.results[key] = expired
	return expired, nil
}

func restoreTestAccount(t testing.TB, status AccountStatus, now time.Time) Account {
	t.Helper()

	account, err := RestoreAccount(AccountSnapshot{
		ID:                 uuid.New(),
		Username:           "admin",
		Status:             status,
		PasswordHash:       currentSessionPasswordHash,
		PasswordAlgorithm:  currentSessionPasswordAlgorithm,
		PasswordParameters: currentSessionPasswordParameters,
		PasswordVersion:    4,
		AdminVersion:       7,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func mustRestoreAccount(t testing.TB, snapshot AccountSnapshot) Account {
	t.Helper()
	account, err := RestoreAccount(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func mustRestoreSession(t testing.TB, snapshot SessionSnapshot) Session {
	t.Helper()
	session, err := RestoreSession(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func newOperationID(t testing.TB, marker byte) idempotency.OperationID {
	t.Helper()
	entropy := make([]byte, 16)
	for index := range entropy {
		entropy[index] = marker
	}
	operationID, err := idempotency.NewOperationID(entropy)
	if err != nil {
		t.Fatal(err)
	}
	return operationID
}

func loadAdminSessionHMACKeyring(t testing.TB, now time.Time) *security.HMACKeyring[security.AdminSessionKeyPurpose] {
	t.Helper()
	path := writeSymmetricKeyring(t, now, "admin-session-keyring.json")
	keyring, err := security.LoadHMACKeyring[security.AdminSessionKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func loadAdminTOTPKeyring(t testing.TB, now time.Time) *security.AESKeyring[security.TOTPKeyPurpose] {
	t.Helper()
	path := writeSymmetricKeyring(t, now, "admin-totp-keyring.json")
	keyring, err := security.LoadAESKeyring[security.TOTPKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func testEnvelopeCipher(t testing.TB, now time.Time) *secretresult.EnvelopeCipher {
	t.Helper()

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	document := struct {
		ActiveVersion uint32 `json:"active_version"`
		Keys          []struct {
			Version   uint32    `json:"version"`
			Key       string    `json:"key"`
			NotBefore time.Time `json:"not_before"`
		} `json:"keys"`
	}{ActiveVersion: 7}
	document.Keys = append(document.Keys, struct {
		Version   uint32    `json:"version"`
		Key       string    `json:"key"`
		NotBefore time.Time `json:"not_before"`
	}{
		Version: 7, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour),
	})
	path := writeJSONKeyring(t, "admin-result-envelope-keyring.json", document)
	keyring, err := security.LoadAESKeyring[security.ResultEnvelopeKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secretresult.NewEnvelopeCipher(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func writeSymmetricKeyring(t testing.TB, now time.Time, name string) string {
	t.Helper()

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	document := struct {
		ActiveVersion uint32 `json:"active_version"`
		Keys          []struct {
			Version   uint32    `json:"version"`
			Key       string    `json:"key"`
			NotBefore time.Time `json:"not_before"`
		} `json:"keys"`
	}{ActiveVersion: 1}
	document.Keys = append(document.Keys, struct {
		Version   uint32    `json:"version"`
		Key       string    `json:"key"`
		NotBefore time.Time `json:"not_before"`
	}{
		Version: 1, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour),
	})
	return writeJSONKeyring(t, name, document)
}

func writeJSONKeyring(t testing.TB, name string, document any) string {
	t.Helper()

	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	return path
}
