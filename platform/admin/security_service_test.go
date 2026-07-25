package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iFTY-R/game-night/platform/audit"
	"github.com/iFTY-R/game-night/platform/challenge"
	"github.com/iFTY-R/game-night/platform/clock"
	"github.com/iFTY-R/game-night/platform/idempotency"
	"github.com/iFTY-R/game-night/platform/identifier"
	"github.com/iFTY-R/game-night/platform/outbox"
	"github.com/iFTY-R/game-night/platform/ratelimit"
	"github.com/iFTY-R/game-night/platform/secretresult"
	"github.com/iFTY-R/game-night/platform/security"
)

const serviceTestPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestLoginPasswordDerivesSessionKindFromEnrollmentState(t *testing.T) {
	now := time.Date(2026, time.July, 25, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		withMFA     bool
		wantFull    bool
		wantMFA     bool
		wantInitial bool
	}{
		{name: "full without active enrollment", wantFull: true},
		{name: "mfa pending with active enrollment", withMFA: true, wantMFA: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSecurityFixture(t, now, AccountStatusActive)
			fixture.hasher.setSecret(serviceTestPasswordHash, "current-password")
			if test.withMFA {
				fixture.enrollments.active = &Enrollment{snapshot: EnrollmentSnapshot{
					ID: uuid.New(), AdminID: fixture.account.Snapshot().ID, Ciphertext: []byte{1}, Nonce: []byte{2},
					KeyVersion: 1, Status: EnrollmentStatusActive, AdminVersion: fixture.account.Snapshot().AdminVersion,
					EnrollmentVersion: 3, ReplayFloor: int64Ptr(10), OperationID: "active-mfa", CreatedAt: now.Add(-time.Hour), ActivatedAt: now.Add(-time.Minute),
				}}
			}
			issued, err := fixture.service.BeginAdminLogin(t.Context(), AdminChallengeRequest{
				CanonicalOrigin: "https://admin.example", RequestFlowID: "flow-login",
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := fixture.service.LoginPassword(t.Context(), LoginPasswordCommand{
				Credentials: issued.Credentials, Password: "current-password",
				OperationID: mustOperationID(t, 0x11), RequestDigest: digestAdminRequest("test.login", test.name),
				CanonicalOrigin: "https://admin.example", RequestFlowID: "flow-login", ClientIP: "203.0.113.10", UserAgent: "svc-test",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.RequiresInitialPasswordChange; got != test.wantInitial {
				t.Fatalf("RequiresInitialPasswordChange = %t, want %t", got, test.wantInitial)
			}
			if got := result.RequiresMFA; got != test.wantMFA {
				t.Fatalf("RequiresMFA = %t, want %t", got, test.wantMFA)
			}
			if got := result.Session.Session.Snapshot().Kind == SessionKindFull; got != test.wantFull {
				t.Fatalf("full session = %t, want %t", got, test.wantFull)
			}
		})
	}
}

func TestChangeAdminPasswordRevokesOlderSessionsAndReplaysReceipt(t *testing.T) {
	now := time.Date(2026, time.July, 25, 11, 0, 0, 0, time.UTC)
	fixture := newSecurityFixture(t, now, AccountStatusActive)
	fixture.hasher.setSecret(serviceTestPasswordHash, "current-password")
	current := fixture.issueSession(t, SessionKindFull, fixture.account.Snapshot().AdminVersion, fixture.account.Snapshot().PasswordVersion)
	other := fixture.issueSession(t, SessionKindFull, fixture.account.Snapshot().AdminVersion, fixture.account.Snapshot().PasswordVersion)
	fixture.sessions.mustInsert(current.Session)
	fixture.sessions.mustInsert(other.Session)

	command := ChangePasswordCommand{
		Session: current.Session, SessionToken: current.Token, CSRFToken: current.CSRFToken,
		Current: "current-password", New: "new-password", ClientIP: "203.0.113.10",
		OperationID: mustOperationID(t, 0x21), ExpectedPasswordVersion: fixture.account.Snapshot().PasswordVersion,
	}
	first, err := fixture.service.ChangeAdminPassword(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if first.RevokedSessions != 2 {
		t.Fatalf("revoked sessions = %d, want 2", first.RevokedSessions)
	}
	if first.Session.Session.Snapshot().ID == current.Session.Snapshot().ID {
		t.Fatal("password change reused the previous session")
	}
	command.Session = first.Session.Session
	command.SessionToken = first.Session.Token
	command.CSRFToken = first.Session.CSRFToken
	second, err := fixture.service.ChangeAdminPassword(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if second.Session.Session.Snapshot().ID != first.Session.Session.Snapshot().ID || second.RevokedSessions != first.RevokedSessions {
		t.Fatalf("replayed change password result = %+v, want %+v", second, first)
	}
}

func TestElevateAdminSessionUsesPasswordOnlyWhenMFAIsDisabled(t *testing.T) {
	now := time.Date(2026, time.July, 25, 11, 30, 0, 0, time.UTC)
	fixture := newSecurityFixture(t, now, AccountStatusActive)
	fixture.hasher.setSecret(serviceTestPasswordHash, "current-password")
	current := fixture.issueSession(t, SessionKindFull, fixture.account.Snapshot().AdminVersion, fixture.account.Snapshot().PasswordVersion)
	fixture.sessions.mustInsert(current.Session)

	result, err := fixture.service.ElevateAdminSession(t.Context(), ElevateSessionCommand{
		Session: current.Session, SessionToken: current.Token, CSRFToken: current.CSRFToken,
		OperationID: mustOperationID(t, 0x29), Scope: ElevationScopeSecurityRevokeSessions,
		CurrentPassword: "current-password", ClientIP: "203.0.113.10",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UsedRecoveryCode || result.Elevation.Snapshot().EnrollmentVersion != 0 {
		t.Fatalf("password-only elevation = %+v", result)
	}
	if err := result.Elevation.Validate(current.Session, 0, ElevationScopeSecurityRevokeSessions, now); err != nil {
		t.Fatalf("validate password-only elevation: %v", err)
	}
}

func TestCompleteTotpEnrollmentActivatesPendingSeedAndReturnsRecoveryBundle(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	fixture := newSecurityFixture(t, now, AccountStatusActive)
	fixture.hasher.setSecret(serviceTestPasswordHash, "current-password")
	current := fixture.issueSession(t, SessionKindFull, fixture.account.Snapshot().AdminVersion, fixture.account.Snapshot().PasswordVersion)
	other := fixture.issueSession(t, SessionKindFull, fixture.account.Snapshot().AdminVersion, fixture.account.Snapshot().PasswordVersion)
	fixture.sessions.mustInsert(current.Session)
	fixture.sessions.mustInsert(other.Session)

	begin, err := fixture.service.BeginTotpEnrollment(t.Context(), BeginEnrollmentCommand{
		Session: current.Session, SessionToken: current.Token, CSRFToken: current.CSRFToken,
		OperationID: mustOperationID(t, 0x31), CurrentPassword: "current-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := GenerateTOTPCode(begin.Secret, now)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := fixture.service.CompleteTotpEnrollment(t.Context(), CompleteEnrollmentCommand{
		Session: current.Session, SessionToken: current.Token, CSRFToken: current.CSRFToken,
		EnrollmentOperationID:    begin.Enrollment.Snapshot().OperationID,
		RecoveryCodesOperationID: mustOperationID(t, 0x32),
		TOTPPasscode:             code,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Session.Session.Snapshot().Kind != SessionKindFull {
		t.Fatalf("completed session kind = %s", completed.Session.Session.Snapshot().Kind)
	}
	if len(completed.RecoveryCodes) != AdminRecoveryCodeCount {
		t.Fatalf("recovery code count = %d, want %d", len(completed.RecoveryCodes), AdminRecoveryCodeCount)
	}
	if completed.RevokedSessions != 2 {
		t.Fatalf("revoked sessions = %d, want 2", completed.RevokedSessions)
	}
	if fixture.enrollments.active == nil || !fixture.enrollments.active.Active() {
		t.Fatal("pending enrollment was not activated")
	}
	recoveryState, err := fixture.recoveryCodes.GetActiveSetState(t.Context(), fixture.account.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveryState.SetVersion != 1 || recoveryState.RemainingActive != AdminRecoveryCodeCount {
		t.Fatalf("recovery-code state = %+v, want initial version with all codes active", recoveryState)
	}
	currentView, err := fixture.service.GetCurrentAdminSession(t.Context(), CurrentSessionCommand{
		Session: completed.Session.Session, SessionToken: completed.Session.Token, CSRFToken: completed.Session.CSRFToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if currentView.View.RecoveryCodes != recoveryState {
		t.Fatalf("session recovery-code state = %+v, want %+v", currentView.View.RecoveryCodes, recoveryState)
	}
}

func TestRevokeOtherAdminSessionsRequiresMatchingPreviewAndElevation(t *testing.T) {
	now := time.Date(2026, time.July, 25, 13, 0, 0, 0, time.UTC)
	fixture := newSecurityFixture(t, now, AccountStatusActive)
	current := fixture.issueSession(t, SessionKindFull, fixture.account.Snapshot().AdminVersion, fixture.account.Snapshot().PasswordVersion)
	other := fixture.issueSession(t, SessionKindFull, fixture.account.Snapshot().AdminVersion, fixture.account.Snapshot().PasswordVersion)
	fixture.sessions.mustInsert(current.Session)
	fixture.sessions.mustInsert(other.Session)
	elevation, err := NewElevation(current.Session, 0, ElevationScopeSecurityRevokeSessions, now, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	fixture.elevations.mustUpsert(elevation)

	preview, err := fixture.service.PreviewRevokeOtherAdminSessions(t.Context(), PreviewRevokeOtherSessionsCommand{
		Session: current.Session, SessionToken: current.Token, CSRFToken: current.CSRFToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.RevokeOtherAdminSessions(t.Context(), RevokeOtherSessionsCommand{
		Session: current.Session, SessionToken: current.Token, CSRFToken: current.CSRFToken,
		OperationID: mustOperationID(t, 0x41), PreviewVersion: preview.PreviewVersion,
		ExpectedAdminVersion: preview.CurrentAdminVersion, ExpectedCurrentSessionVersion: preview.CurrentSessionVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RevokedSessions != 1 {
		t.Fatalf("revoked sessions = %d, want 1", result.RevokedSessions)
	}
	replayed, err := fixture.service.RevokeOtherAdminSessions(t.Context(), RevokeOtherSessionsCommand{
		Session: current.Session, SessionToken: current.Token, CSRFToken: current.CSRFToken,
		OperationID: mustOperationID(t, 0x41), PreviewVersion: preview.PreviewVersion,
		ExpectedAdminVersion: preview.CurrentAdminVersion, ExpectedCurrentSessionVersion: preview.CurrentSessionVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.RevokedSessions != result.RevokedSessions {
		t.Fatalf("replayed revoke result = %+v, want %+v", replayed, result)
	}
}

type securityFixture struct {
	now      time.Time
	service  *Service
	account  Account
	hasher   *memoryHasher
	accounts *memoryAccountRepository

	challenges    *memoryChallengeRepository
	enrollments   *memoryEnrollmentRepository
	sessions      *memorySessionRepository
	secretResults *memorySecretResultRepository
	recoveryCodes *memoryRecoveryCodeRepository
	elevations    *memoryElevationRepository
	receipts      *memoryReceiptRepository
}

func newSecurityFixture(t testing.TB, now time.Time, status AccountStatus) *securityFixture {
	t.Helper()
	account := mustRestoreAccount(t, AccountSnapshot{
		ID: uuid.New(), Username: "admin", Status: status,
		PasswordHash: serviceTestPasswordHash, PasswordAlgorithm: PasswordAlgorithmArgon2id, PasswordParameters: `{"MemoryKiB":65536,"Iterations":3,"Parallelism":2,"SaltLength":16,"KeyLength":32}`,
		PasswordVersion: 4, AdminVersion: 7, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
	})
	hasher := newMemoryHasher()
	accounts := &memoryAccountRepository{account: account}
	challenges := &memoryChallengeRepository{}
	enrollments := &memoryEnrollmentRepository{}
	sessions := newMemorySessionRepository()
	secretResults := newMemorySecretResultRepository()
	recoveryCodes := newMemoryRecoveryCodeRepository()
	elevations := newMemoryElevationRepository()
	receipts := newMemoryReceiptRepository()
	unitOfWork := memoryAdminUnitOfWork{
		accounts: accounts, challenges: challenges, enrollments: enrollments, sessions: sessions,
		secretResults: secretResults, recoveryCodes: recoveryCodes, elevations: elevations, receipts: receipts,
	}
	challengeService := newAdminChallengeService(t, now)
	sessionKeys := loadAdminSessionHMACKeyringLocal(t, now)
	sessionService, err := NewSessionService(sessionKeys, clock.NewFake(now))
	if err != nil {
		t.Fatal(err)
	}
	totpService, err := NewTOTPService(loadAdminTOTPKeyringLocal(t, now))
	if err != nil {
		t.Fatal(err)
	}
	recoveryService, err := NewRecoveryCodeService(hasher)
	if err != nil {
		t.Fatal(err)
	}
	resultService, err := secretresult.NewServiceWithAdminAccess(testEnvelopeCipherLocal(t, now), clock.NewFake(now), sessionKeys)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceDependencies{
		Challenge: challengeService, Passwords: hasher, PasswordPolicy: DefaultPasswordPolicy(),
		TOTP: totpService, Sessions: sessionService, RecoveryCodes: recoveryService, Results: resultService,
		Clock: clock.NewFake(now), UnitOfWork: unitOfWork, Limiter: allowAllLimiter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &securityFixture{
		now: now, service: service, account: account, hasher: hasher, accounts: accounts,
		challenges: challenges, enrollments: enrollments, sessions: sessions, secretResults: secretResults,
		recoveryCodes: recoveryCodes, elevations: elevations, receipts: receipts,
	}
}

func (fixture *securityFixture) issueSession(t testing.TB, kind SessionKind, adminVersion, passwordVersion int64) IssuedSession {
	t.Helper()
	issued, err := fixture.service.sessions.IssueWithClient(
		fixture.account.Snapshot().ID,
		kind,
		adminVersion,
		passwordVersion,
		SessionClientMetadata{ClientIP: "203.0.113.10", UserAgent: "svc-test"},
		fixture.now,
	)
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

type memoryHasher struct {
	hashToSecret map[string]string
}

func newMemoryHasher() *memoryHasher {
	return &memoryHasher{hashToSecret: map[string]string{}}
}

func (hasher *memoryHasher) setSecret(hash, secret string) {
	hasher.hashToSecret[hash] = secret
}

func (hasher *memoryHasher) Hash(_ context.Context, secret []byte) (string, error) {
	hasher.hashToSecret[serviceTestPasswordHash] = string(secret)
	return serviceTestPasswordHash, nil
}

func (hasher *memoryHasher) VerifyOrDummy(_ context.Context, hash string, secret []byte) (bool, bool, error) {
	return hasher.hashToSecret[hash] == string(secret), false, nil
}

type allowAllLimiter struct{}

func (allowAllLimiter) Consume(context.Context, ratelimit.ConsumptionRequest) (ratelimit.Decision, error) {
	return ratelimit.Allow(), nil
}

type memoryAdminUnitOfWork struct {
	accounts      *memoryAccountRepository
	challenges    *memoryChallengeRepository
	enrollments   *memoryEnrollmentRepository
	sessions      *memorySessionRepository
	secretResults *memorySecretResultRepository
	recoveryCodes *memoryRecoveryCodeRepository
	elevations    *memoryElevationRepository
	receipts      *memoryReceiptRepository
}

func (unitOfWork memoryAdminUnitOfWork) Run(ctx context.Context, work TransactionWork) error {
	return work(ctx, memoryAdminTransaction{
		accounts: unitOfWork.accounts, challenges: unitOfWork.challenges, enrollments: unitOfWork.enrollments, sessions: unitOfWork.sessions,
		secretResults: unitOfWork.secretResults, recoveryCodes: unitOfWork.recoveryCodes, elevations: unitOfWork.elevations, receipts: unitOfWork.receipts,
	})
}

type memoryAdminTransaction struct {
	accounts      *memoryAccountRepository
	challenges    *memoryChallengeRepository
	enrollments   *memoryEnrollmentRepository
	sessions      *memorySessionRepository
	secretResults *memorySecretResultRepository
	recoveryCodes *memoryRecoveryCodeRepository
	elevations    *memoryElevationRepository
	receipts      *memoryReceiptRepository
}

func (transaction memoryAdminTransaction) Challenges() ChallengeRepository {
	return transaction.challenges
}
func (transaction memoryAdminTransaction) SecretResults() secretresult.Repository {
	return transaction.secretResults
}
func (transaction memoryAdminTransaction) Accounts() AccountRepository { return transaction.accounts }
func (transaction memoryAdminTransaction) Enrollments() EnrollmentRepository {
	return transaction.enrollments
}
func (transaction memoryAdminTransaction) Sessions() SessionRepository { return transaction.sessions }
func (transaction memoryAdminTransaction) Elevations() ElevationRepository {
	return transaction.elevations
}
func (transaction memoryAdminTransaction) CommandReceipts() CommandReceiptRepository {
	return transaction.receipts
}
func (transaction memoryAdminTransaction) RecoveryCodes() RecoveryCodeRepository {
	return transaction.recoveryCodes
}
func (memoryAdminTransaction) Audit() audit.Repository                      { return nil }
func (memoryAdminTransaction) AuditCheckpoints() audit.CheckpointRepository { return nil }
func (memoryAdminTransaction) OutboxEvents() outbox.EventRepository         { return nil }

type memoryAccountRepository struct {
	account Account
}

func (repository *memoryAccountRepository) GetForUpdate(context.Context) (Account, error) {
	return repository.account, nil
}

func (repository *memoryAccountRepository) BootstrapPasswordCAS(_ context.Context, current Account, hash, algorithm, parameters string, at time.Time) (Account, error) {
	updated, err := current.WithPassword(hash, algorithm, parameters, at)
	if err != nil {
		return Account{}, err
	}
	repository.account = updated
	return updated, nil
}

func (repository *memoryAccountRepository) UpdatePasswordCAS(_ context.Context, current Account, hash, algorithm, parameters string, at time.Time) (Account, error) {
	updated, err := current.WithPassword(hash, algorithm, parameters, at)
	if err != nil {
		return Account{}, err
	}
	repository.account = updated
	return updated, nil
}

func (repository *memoryAccountRepository) TransitionStatusCAS(_ context.Context, current Account, next AccountStatus, at time.Time) (Account, error) {
	updated, err := current.Transition(next, at)
	if err != nil {
		return Account{}, err
	}
	repository.account = updated
	return updated, nil
}

func (repository *memoryAccountRepository) RecordMFAChangeCAS(_ context.Context, current Account, at time.Time) (Account, error) {
	updated, err := current.RecordMFAChange(at)
	if err != nil {
		return Account{}, err
	}
	repository.account = updated
	return updated, nil
}

type memoryEnrollmentRepository struct {
	pending *Enrollment
	active  *Enrollment
}

func (repository *memoryEnrollmentRepository) CreatePending(_ context.Context, enrollment Enrollment) (Enrollment, error) {
	repository.pending = &enrollment
	return enrollment, nil
}

func (repository *memoryEnrollmentRepository) GetPendingForUpdate(_ context.Context, _ uuid.UUID) (Enrollment, error) {
	if repository.pending == nil {
		return Enrollment{}, ErrNotFound
	}
	return *repository.pending, nil
}

func (repository *memoryEnrollmentRepository) GetActiveForUpdate(_ context.Context, _ uuid.UUID) (Enrollment, error) {
	if repository.active == nil {
		return Enrollment{}, ErrNotFound
	}
	return *repository.active, nil
}

func (repository *memoryEnrollmentRepository) ActivateCAS(_ context.Context, current Enrollment, step, nextAdminVersion int64, at time.Time) (Enrollment, error) {
	updated, err := current.Activate(step, nextAdminVersion, at)
	if err != nil {
		return Enrollment{}, err
	}
	repository.pending = nil
	repository.active = &updated
	return updated, nil
}

func (repository *memoryEnrollmentRepository) AcceptTOTPCAS(_ context.Context, current Enrollment, step int64, at time.Time) (Enrollment, error) {
	updated, err := current.AcceptTOTP(step, at)
	if err != nil {
		return Enrollment{}, err
	}
	repository.active = &updated
	return updated, nil
}

func (repository *memoryEnrollmentRepository) DisableCAS(_ context.Context, current Enrollment, nextAdminVersion int64, at time.Time) (Enrollment, error) {
	updated, err := current.Disable(nextAdminVersion, at)
	if err != nil {
		return Enrollment{}, err
	}
	repository.active = nil
	return updated, nil
}

type memorySessionRepository struct {
	byID       map[uuid.UUID]Session
	bySelector map[string]uuid.UUID
}

func newMemorySessionRepository() *memorySessionRepository {
	return &memorySessionRepository{byID: map[uuid.UUID]Session{}, bySelector: map[string]uuid.UUID{}}
}

func (repository *memorySessionRepository) mustInsert(session Session) {
	repository.byID[session.Snapshot().ID] = session
	repository.bySelector[session.Snapshot().Selector] = session.Snapshot().ID
}

func (repository *memorySessionRepository) Insert(_ context.Context, session Session) error {
	repository.mustInsert(session)
	return nil
}

func (repository *memorySessionRepository) GetForUpdate(_ context.Context, selector string) (Session, error) {
	id, ok := repository.bySelector[selector]
	if !ok {
		return Session{}, ErrNotFound
	}
	return repository.byID[id], nil
}

func (repository *memorySessionRepository) GetByIDForUpdate(_ context.Context, sessionID uuid.UUID) (Session, error) {
	session, ok := repository.byID[sessionID]
	if !ok {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (repository *memorySessionRepository) ListActiveForAdmin(_ context.Context, adminID uuid.UUID, at time.Time) ([]Session, error) {
	sessions := make([]Session, 0, len(repository.byID))
	for _, session := range repository.byID {
		if session.Snapshot().AdminID == adminID && session.Active(at) {
			sessions = append(sessions, session)
		}
	}
	return sessions, nil
}

func (repository *memorySessionRepository) TouchCAS(_ context.Context, current Session, at time.Time, idleTTL time.Duration) (Session, error) {
	updated, err := current.Touch(at, idleTTL)
	if err != nil {
		return Session{}, err
	}
	repository.mustInsert(updated)
	return updated, nil
}

func (repository *memorySessionRepository) RevokeCAS(_ context.Context, current Session, reason string, at time.Time) (Session, error) {
	updated, err := current.Revoke(reason, at)
	if err != nil {
		return Session{}, err
	}
	repository.mustInsert(updated)
	return updated, nil
}

func (repository *memorySessionRepository) RevokeOtherActiveCAS(_ context.Context, adminID, preservedSessionID uuid.UUID, expectedAdminVersion, expectedPreservedSessionVersion int64, reason string, at time.Time) ([]Session, error) {
	preserved, ok := repository.byID[preservedSessionID]
	if !ok || preserved.Snapshot().AdminVersion != expectedAdminVersion || preserved.Snapshot().SessionVersion != expectedPreservedSessionVersion {
		return nil, ErrConcurrentTransition
	}
	revoked := make([]Session, 0)
	for id, session := range repository.byID {
		if session.Snapshot().AdminID != adminID || id == preservedSessionID || !session.Active(at) {
			continue
		}
		updated, err := session.Revoke(reason, at)
		if err != nil {
			return nil, err
		}
		repository.mustInsert(updated)
		revoked = append(revoked, updated)
	}
	return revoked, nil
}

type memoryChallengeRepository struct {
	record Challenge
}

func (repository *memoryChallengeRepository) Insert(_ context.Context, record Challenge) error {
	repository.record = record
	return nil
}

func (repository *memoryChallengeRepository) GetForUpdate(_ context.Context, selector identifier.Selector) (Challenge, error) {
	if repository.record.Snapshot().Selector != selector {
		return Challenge{}, challenge.ErrNotFound
	}
	return repository.record, nil
}

func (repository *memoryChallengeRepository) RecordFailureCAS(_ context.Context, record Challenge, at time.Time) (Challenge, error) {
	updated, err := record.RecordFailure(at)
	if err != nil {
		return Challenge{}, err
	}
	repository.record = updated
	return updated, nil
}

func (repository *memoryChallengeRepository) ConsumeCAS(_ context.Context, record Challenge, current challenge.SubjectBinding) (Challenge, error) {
	repository.record = record
	return record, nil
}

func (*memoryChallengeRepository) RevokeActiveByAdminID(context.Context, uuid.UUID, time.Time) (int64, error) {
	return 0, nil
}

type memorySecretResultRepository struct {
	results map[secretresult.Key]secretresult.Result
}

func newMemorySecretResultRepository() *memorySecretResultRepository {
	return &memorySecretResultRepository{results: map[secretresult.Key]secretresult.Result{}}
}

func (repository *memorySecretResultRepository) InsertAvailable(_ context.Context, result secretresult.Result) (secretresult.Result, error) {
	repository.results[result.Snapshot().Binding.Key] = result
	return result, nil
}

func (repository *memorySecretResultRepository) GetByOperationForUpdate(_ context.Context, key secretresult.Key) (secretresult.Result, error) {
	result, ok := repository.results[key]
	if !ok {
		return secretresult.Result{}, secretresult.ErrNotFound
	}
	return result, nil
}

func (repository *memorySecretResultRepository) GetByIDForUpdate(_ context.Context, resultID uuid.UUID) (secretresult.Result, error) {
	for _, result := range repository.results {
		if result.Snapshot().ID == resultID {
			return result, nil
		}
	}
	return secretresult.Result{}, secretresult.ErrNotFound
}

func (repository *memorySecretResultRepository) ConfirmCAS(_ context.Context, confirmation secretresult.Confirmation) (secretresult.Result, error) {
	current, ok := repository.results[confirmation.Binding.Key]
	if !ok || current.Snapshot().ID != confirmation.ResultID {
		return secretresult.Result{}, secretresult.ErrConcurrentTransition
	}
	confirmed, err := current.Confirm(confirmation.ConfirmedAt)
	if err != nil {
		return secretresult.Result{}, err
	}
	repository.results[confirmation.Binding.Key] = confirmed
	return confirmed, nil
}

func (repository *memorySecretResultRepository) ExpireCAS(_ context.Context, result secretresult.Result, expiredAt time.Time) (secretresult.Result, error) {
	expired, err := result.Expire(expiredAt)
	if err != nil {
		return secretresult.Result{}, err
	}
	repository.results[result.Snapshot().Binding.Key] = expired
	return expired, nil
}

type memoryRecoveryCodeRepository struct {
	bySelector map[string]RecoveryCode
}

func newMemoryRecoveryCodeRepository() *memoryRecoveryCodeRepository {
	return &memoryRecoveryCodeRepository{bySelector: map[string]RecoveryCode{}}
}

func (repository *memoryRecoveryCodeRepository) Insert(_ context.Context, code RecoveryCode) error {
	repository.bySelector[code.Snapshot().Selector] = code
	return nil
}

func (repository *memoryRecoveryCodeRepository) FindActiveBySelector(_ context.Context, selector string) (RecoveryCode, error) {
	code, ok := repository.bySelector[selector]
	if !ok {
		return RecoveryCode{}, ErrNotFound
	}
	return code, nil
}

func (repository *memoryRecoveryCodeRepository) GetActiveSetState(_ context.Context, adminID uuid.UUID) (RecoveryCodeSetState, error) {
	state := RecoveryCodeSetState{}
	for _, code := range repository.bySelector {
		snapshot := code.Snapshot()
		if snapshot.AdminID == adminID && snapshot.SetVersion > state.SetVersion {
			state.SetVersion = snapshot.SetVersion
			state.RemainingActive = 0
		}
	}
	for _, code := range repository.bySelector {
		snapshot := code.Snapshot()
		if snapshot.AdminID == adminID && snapshot.SetVersion == state.SetVersion && snapshot.Status == RecoveryCodeStatusActive {
			state.RemainingActive++
		}
	}
	return state, nil
}

func (repository *memoryRecoveryCodeRepository) ConsumeCAS(_ context.Context, current RecoveryCode, at time.Time) (RecoveryCode, error) {
	updated, err := current.Consume(at)
	if err != nil {
		return RecoveryCode{}, err
	}
	repository.bySelector[current.Snapshot().Selector] = updated
	return updated, nil
}

func (repository *memoryRecoveryCodeRepository) RevokeSet(_ context.Context, _ uuid.UUID, setVersion int64, at time.Time) (int64, error) {
	var count int64
	for selector, code := range repository.bySelector {
		if code.Snapshot().SetVersion != setVersion || code.Snapshot().Status != RecoveryCodeStatusActive {
			continue
		}
		updated, err := code.Revoke(at)
		if err != nil {
			return 0, err
		}
		repository.bySelector[selector] = updated
		count++
	}
	return count, nil
}

func (repository *memoryRecoveryCodeRepository) RevokeAllSets(_ context.Context, _ uuid.UUID, at time.Time) (int64, error) {
	var count int64
	for selector, code := range repository.bySelector {
		if code.Snapshot().Status != RecoveryCodeStatusActive {
			continue
		}
		updated, err := code.Revoke(at)
		if err != nil {
			return 0, err
		}
		repository.bySelector[selector] = updated
		count++
	}
	return count, nil
}

type memoryElevationRepository struct {
	values map[string]Elevation
}

func newMemoryElevationRepository() *memoryElevationRepository {
	return &memoryElevationRepository{values: map[string]Elevation{}}
}

func (repository *memoryElevationRepository) key(sessionID uuid.UUID, scope ElevationScope) string {
	return sessionID.String() + ":" + string(scope)
}

func (repository *memoryElevationRepository) mustUpsert(elevation Elevation) {
	snapshot := elevation.Snapshot()
	repository.values[repository.key(snapshot.SessionID, snapshot.Scope)] = elevation
}

func (repository *memoryElevationRepository) UpsertLive(_ context.Context, elevation Elevation) (Elevation, error) {
	repository.mustUpsert(elevation)
	return elevation, nil
}

func (repository *memoryElevationRepository) GetForSessionScope(_ context.Context, sessionID uuid.UUID, scope ElevationScope, at time.Time) (Elevation, error) {
	elevation, ok := repository.values[repository.key(sessionID, scope)]
	if !ok {
		return Elevation{}, ErrElevationDenied
	}
	return elevation, nil
}

func (repository *memoryElevationRepository) ListLiveForSessions(_ context.Context, adminID uuid.UUID, sessionIDs []uuid.UUID, at time.Time) ([]Elevation, error) {
	selected := make(map[uuid.UUID]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		selected[sessionID] = struct{}{}
	}
	elevations := make([]Elevation, 0)
	for _, elevation := range repository.values {
		snapshot := elevation.Snapshot()
		_, included := selected[snapshot.SessionID]
		if snapshot.AdminID == adminID && included && snapshot.RevokedAt.IsZero() && at.Before(snapshot.ExpiresAt) {
			elevations = append(elevations, elevation)
		}
	}
	return elevations, nil
}

func (repository *memoryElevationRepository) RevokeCAS(_ context.Context, current Elevation, at time.Time) (Elevation, error) {
	updated, err := current.Revoke(at)
	if err != nil {
		return Elevation{}, err
	}
	repository.mustUpsert(updated)
	return updated, nil
}

type memoryReceiptRepository struct {
	values map[string]CommandReceipt
}

func newMemoryReceiptRepository() *memoryReceiptRepository {
	return &memoryReceiptRepository{values: map[string]CommandReceipt{}}
}

func (repository *memoryReceiptRepository) key(adminID uuid.UUID, operationID idempotency.OperationID) string {
	return adminID.String() + ":" + operationID.Value()
}

func (repository *memoryReceiptRepository) Save(_ context.Context, receipt CommandReceipt) (CommandReceipt, error) {
	key := repository.key(receipt.AdminID, receipt.OperationID)
	if existing, ok := repository.values[key]; ok {
		if existing.RequestDigest != receipt.RequestDigest || existing.Command != receipt.Command || existing.TargetID != receipt.TargetID {
			return CommandReceipt{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	repository.values[key] = receipt
	return receipt, nil
}

func (repository *memoryReceiptRepository) Get(_ context.Context, adminID uuid.UUID, operationID idempotency.OperationID) (CommandReceipt, error) {
	receipt, ok := repository.values[repository.key(adminID, operationID)]
	if !ok {
		return CommandReceipt{}, ErrNotFound
	}
	return receipt, nil
}

func mustOperationID(t testing.TB, marker byte) idempotency.OperationID {
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

func loadAdminSessionHMACKeyringLocal(t testing.TB, now time.Time) *security.HMACKeyring[security.AdminSessionKeyPurpose] {
	t.Helper()
	path := writeSymmetricKeyringLocal(t, now, "admin-session-keyring.json")
	keyring, err := security.LoadHMACKeyring[security.AdminSessionKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func loadAdminTOTPKeyringLocal(t testing.TB, now time.Time) *security.AESKeyring[security.TOTPKeyPurpose] {
	t.Helper()
	path := writeSymmetricKeyringLocal(t, now, "admin-totp-keyring.json")
	keyring, err := security.LoadAESKeyring[security.TOTPKeyPurpose](path, now)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func testEnvelopeCipherLocal(t testing.TB, now time.Time) *secretresult.EnvelopeCipher {
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
	}{Version: 7, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour)})
	path := writeJSONKeyringLocal(t, "admin-result-envelope-keyring.json", document)
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

func writeSymmetricKeyringLocal(t testing.TB, now time.Time, name string) string {
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
	}{Version: 1, Key: base64.StdEncoding.EncodeToString(key), NotBefore: now.Add(-time.Hour)})
	return writeJSONKeyringLocal(t, name, document)
}

func writeJSONKeyringLocal(t testing.TB, name string, document any) string {
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

func mustRestoreAccount(t testing.TB, snapshot AccountSnapshot) Account {
	t.Helper()
	account, err := RestoreAccount(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func int64Ptr(value int64) *int64 { return &value }
