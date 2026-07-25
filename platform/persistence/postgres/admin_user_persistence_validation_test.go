package postgres

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	adminuser "github.com/iFTY-R/game-night/platform/admin/user"
)

func TestAdminUserTextValidationUsesPostgresCharacterLengths(t *testing.T) {
	name := strings.Repeat("标", 64)
	storedName, normalized, ok := validTagDefinition(name, "#12ABEF")
	if !ok || storedName != name || normalized != name {
		t.Fatalf("64-character tag rejected: name=%q normalized=%q ok=%v", storedName, normalized, ok)
	}
	if _, _, ok = validTagDefinition(strings.Repeat("标", 65), "#12ABEF"); ok {
		t.Fatal("65-character tag was accepted")
	}
	if !validAdminReason(strings.Repeat("由", 512)) || validAdminReason(strings.Repeat("由", 513)) {
		t.Fatal("administrator reason limit does not use Unicode characters")
	}
	if !validAdminNoteBody(strings.Repeat("注", 4000)) || validAdminNoteBody(strings.Repeat("注", 4001)) {
		t.Fatal("note body limit does not use Unicode characters")
	}
	if validAdminReason(string([]byte{0xff})) || validAdminNoteBody(string([]byte{0xff})) {
		t.Fatal("invalid UTF-8 text was accepted")
	}
	_, normalizedPrefix, ok := normalizeAdminTagName("  ＶＩＰ  ")
	if !ok || normalizedPrefix != "vip" {
		t.Fatalf("tag prefix normalized to %q, ok=%v", normalizedPrefix, ok)
	}
}

func TestAdminJobValidationRejectsEmptyTargetSet(t *testing.T) {
	if validStartBatchJob(adminuser.StartBatchJobCommand{
		BatchJobID: uuid.New(), ActorAdminID: uuid.New(), OperationID: "empty-targets", PreviewID: uuid.New(),
		Reason: "reviewed empty batch", CreatedAt: time.Now().UTC(),
	}) {
		t.Fatal("empty target set would create a permanently queued batch job")
	}
}

func TestAdminJobValidationRequiresStableFailureKeys(t *testing.T) {
	item := adminuser.BatchItem{
		ID: uuid.New(), BatchJobID: uuid.New(), State: "running", LeaseOwner: "worker-a", Version: 2,
	}
	now := time.Now().UTC()
	if validBatchCompletion(item, "failed", "", now) || validBatchCompletion(item, "failed", "Worker exploded", now) {
		t.Fatal("batch failure accepted an absent or free-form error message")
	}
	if !validBatchCompletion(item, "failed", "admin.batch.version_changed", now) {
		t.Fatal("batch failure rejected a stable error key")
	}
	if validBatchCompletion(item, "succeeded", "admin.batch.unexpected", now) {
		t.Fatal("successful batch item retained a failure key")
	}
}

func TestAdminExportFailureValidationRequiresStableFailureKey(t *testing.T) {
	command := adminuser.FailExportJobCommand{
		Job:          adminuser.ExportJob{ID: uuid.New(), State: "running", LeaseOwner: "worker-a", Version: 2},
		MatchedUsers: 2, ExportedUsers: 1, FailedUsers: 1, ErrorMessageKey: "admin.export.object_write_failed",
		CompletedAt: time.Now().UTC(),
	}
	if !validExportFailure(command) {
		t.Fatal("valid export failure was rejected")
	}
	command.ErrorMessageKey = "object write failed"
	if validExportFailure(command) {
		t.Fatal("free-form export failure message was accepted")
	}
}
