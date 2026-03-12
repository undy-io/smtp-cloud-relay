package spool

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"golang.org/x/sys/unix"
)

func TestNewPayloadStoreCreatesLayoutAndCleansStaging(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, stagingDirName, "stale-dir"), spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, stagingDirName, "stale-file"), []byte("stale"), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, payloadsDirName, publishTempPrefix+"stale"), spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, payloadsDirName, publishTempPrefix+"stale", messageFileName), []byte(`{"textBody":"stale"}`), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	store, err := NewPayloadStore(root)
	if err != nil {
		t.Fatalf("NewPayloadStore() error: %v", err)
	}

	for _, path := range []string{store.payloadsRoot, store.orphanRoot, store.stagingRoot} {
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%q) error: %v", path, err)
		} else if !info.IsDir() {
			t.Fatalf("expected %q to be a directory", path)
		}
	}

	entries, err := os.ReadDir(store.stagingRoot)
	if err != nil {
		t.Fatalf("ReadDir(%q) error: %v", store.stagingRoot, err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected staging root to be empty, got %#v", entries)
	}

	payloadEntries, err := os.ReadDir(store.payloadsRoot)
	if err != nil {
		t.Fatalf("ReadDir(%q) error: %v", store.payloadsRoot, err)
	}
	if len(payloadEntries) != 0 {
		t.Fatalf("expected payload root publish temps to be cleaned, got %#v", payloadEntries)
	}
}

func TestNewPayloadStoreRejectsSymlinkedRootWithoutTouchingTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	root := filepath.Join(base, "spool-link")
	if err := os.Symlink(target, root); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	_, err := NewPayloadStore(root)
	if err == nil {
		t.Fatal("expected NewPayloadStore() to fail for symlinked root")
	}
	for _, name := range []string{payloadsDirName, payloadOrphansDirName, stagingDirName} {
		if _, statErr := os.Stat(filepath.Join(target, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("expected symlink target to remain untouched for %q, stat err=%v", name, statErr)
		}
	}
}

func TestPayloadStoreRejectsInvalidRecordIDBeforeFilesystemUse(t *testing.T) {
	store := newPayloadTestStore(t)

	if err := store.Save("record-1", email.Message{To: []string{"to@example.com"}, TextBody: "text"}); err == nil {
		t.Fatal("expected Save() to reject invalid record id")
	}
	if _, err := store.Load("record-1"); err == nil {
		t.Fatal("expected Load() to reject invalid record id")
	}
}

func TestPayloadStoreRoundTrip(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	msg := email.Message{
		EnvelopeFrom: "envelope@example.com",
		HeaderFrom:   "header@example.com",
		ReplyTo:      []string{"reply@example.com"},
		To:           []string{"one@example.com", "two@example.com"},
		Subject:      "subject",
		TextBody:     "text body",
		HTMLBody:     "<p>html body</p>",
		Attachments: []email.Attachment{
			{Filename: "note.txt", ContentType: "text/plain", Data: []byte("hello world")},
			{Filename: "data.bin", ContentType: "application/octet-stream", Data: []byte{0x01, 0x02, 0x03}},
		},
	}

	if err := store.Save(recordID, msg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	got, err := store.Load(recordID)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if got.EnvelopeFrom != msg.EnvelopeFrom || got.HeaderFrom != msg.HeaderFrom {
		t.Fatalf("unexpected sender fields: %#v", got)
	}
	if strings.Join(got.ReplyTo, ",") != strings.Join(msg.ReplyTo, ",") {
		t.Fatalf("unexpected reply-to: %#v", got.ReplyTo)
	}
	if strings.Join(got.To, ",") != strings.Join(msg.To, ",") {
		t.Fatalf("unexpected recipients: %#v", got.To)
	}
	if got.Subject != msg.Subject || got.TextBody != msg.TextBody || got.HTMLBody != msg.HTMLBody {
		t.Fatalf("unexpected bodies: %#v", got)
	}
	if len(got.Attachments) != len(msg.Attachments) {
		t.Fatalf("unexpected attachment count: %d", len(got.Attachments))
	}
	for i := range msg.Attachments {
		if got.Attachments[i].Filename != msg.Attachments[i].Filename {
			t.Fatalf("unexpected attachment[%d] filename: %q", i, got.Attachments[i].Filename)
		}
		if got.Attachments[i].ContentType != msg.Attachments[i].ContentType {
			t.Fatalf("unexpected attachment[%d] content type: %q", i, got.Attachments[i].ContentType)
		}
		if !bytes.Equal(got.Attachments[i].Data, msg.Attachments[i].Data) {
			t.Fatalf("unexpected attachment[%d] data: %q", i, got.Attachments[i].Data)
		}
	}
}

func TestPayloadStoreLeavesNoStagingArtifactsOnSuccess(t *testing.T) {
	store := newPayloadTestStore(t)
	if err := store.Save(testRecordID(1), email.Message{To: []string{"to@example.com"}, TextBody: "text"}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	entries, err := os.ReadDir(store.stagingRoot)
	if err != nil {
		t.Fatalf("ReadDir(%q) error: %v", store.stagingRoot, err)
	}
	if len(entries) != 0 {
		t.Fatalf("unexpected staging artifacts: %#v", entries)
	}
}

func TestPayloadStoreFailedPublishDoesNotLeavePartialPayload(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	store.renameDirectory = func(sourceParentFD int, sourceName string, targetParentFD int, targetName string) error {
		return errors.New("publish failed")
	}

	err := store.Save(recordID, email.Message{
		To:       []string{"to@example.com"},
		TextBody: "text",
		Attachments: []email.Attachment{{
			Filename:    "note.txt",
			ContentType: "text/plain",
			Data:        []byte("hello"),
		}},
	})
	if err == nil {
		t.Fatal("expected Save() to fail")
	}
	if _, statErr := os.Stat(store.payloadDir(recordID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected no published payload, stat err=%v", statErr)
	}
	entries, readErr := os.ReadDir(store.stagingRoot)
	if readErr != nil {
		t.Fatalf("ReadDir(%q) error: %v", store.stagingRoot, readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected staging cleanup after failed publish, got %#v", entries)
	}
}

func TestPayloadStoreSaveEXDEVFallbackPublishesAndCleansTemps(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	renameCalls := 0
	store.renameDirectory = func(sourceParentFD int, sourceName string, targetParentFD int, targetName string) error {
		renameCalls++
		if renameCalls == 1 {
			return unix.EXDEV
		}
		return renameDirectoryAt(sourceParentFD, sourceName, targetParentFD, targetName)
	}

	if err := store.Save(recordID, email.Message{
		To:       []string{"to@example.com"},
		TextBody: "text",
		Attachments: []email.Attachment{{
			Filename:    "note.txt",
			ContentType: "text/plain",
			Data:        []byte("hello"),
		}},
	}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if renameCalls < 2 {
		t.Fatalf("expected EXDEV fallback to perform a second rename, got %d calls", renameCalls)
	}
	if _, err := store.Load(recordID); err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	stagingEntries, err := os.ReadDir(store.stagingRoot)
	if err != nil {
		t.Fatalf("ReadDir(%q) error: %v", store.stagingRoot, err)
	}
	if len(stagingEntries) != 0 {
		t.Fatalf("expected staging root to be empty, got %#v", stagingEntries)
	}
	payloadEntries, err := os.ReadDir(store.payloadsRoot)
	if err != nil {
		t.Fatalf("ReadDir(%q) error: %v", store.payloadsRoot, err)
	}
	for _, entry := range payloadEntries {
		if strings.HasPrefix(entry.Name(), publishTempPrefix) {
			t.Fatalf("expected no publish temp leftovers, got %q", entry.Name())
		}
	}
}

func TestPayloadStoreCorruptManifestReturnsCorruptionError(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	if err := store.Save(recordID, email.Message{To: []string{"to@example.com"}, TextBody: "text"}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	manifestPath := filepath.Join(store.payloadDir(recordID), messageFileName)
	if err := os.WriteFile(manifestPath, []byte("{not-json"), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	_, err := store.Load(recordID)
	assertPayloadCorruptionPath(t, err, manifestPath)
}

func TestPayloadStoreMissingAttachmentReturnsCorruptionError(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	msg := email.Message{
		To:       []string{"to@example.com"},
		TextBody: "text",
		Attachments: []email.Attachment{
			{Filename: "note.txt", ContentType: "text/plain", Data: []byte("hello")},
		},
	}
	if err := store.Save(recordID, msg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	attachmentPath := filepath.Join(store.payloadDir(recordID), attachmentsDirName, "0.bin")
	if err := os.Remove(attachmentPath); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}

	_, err := store.Load(recordID)
	assertPayloadCorruptionPath(t, err, attachmentPath)
}

func TestPayloadStoreInvalidAttachmentPathReturnsCorruptionError(t *testing.T) {
	recordID := testRecordID(1)
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "message file", path: messageFileName},
		{name: "subdirectory", path: "attachments/subdir/0.bin"},
		{name: "wrong index", path: "attachments/999.bin"},
		{name: "escape", path: "../outside.bin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPayloadTestStore(t)
			saveAttachmentPayload(t, store, recordID)

			manifest := readPayloadManifest(t, store, recordID)
			manifest.Attachments[0].Path = tc.path
			writePayloadManifest(t, store, recordID, manifest)

			_, err := store.Load(recordID)
			assertPayloadCorruptionPath(t, err, filepath.Join(store.payloadDir(recordID), messageFileName))
		})
	}
}

func TestPayloadStoreInvalidAttachmentSHA256ReturnsCorruptionError(t *testing.T) {
	recordID := testRecordID(1)
	for _, tc := range []struct {
		name   string
		sha256 string
	}{
		{name: "malformed hex", sha256: "not-hex"},
		{name: "wrong length", sha256: strings.Repeat("a", 62)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPayloadTestStore(t)
			saveAttachmentPayload(t, store, recordID)

			manifest := readPayloadManifest(t, store, recordID)
			manifest.Attachments[0].SHA256 = tc.sha256
			writePayloadManifest(t, store, recordID, manifest)

			_, err := store.Load(recordID)
			assertPayloadCorruptionPath(t, err, filepath.Join(store.payloadDir(recordID), messageFileName))
		})
	}
}

func TestPayloadStoreTransientIOErrorsStayGeneric(t *testing.T) {
	recordID := testRecordID(1)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, store *PayloadStore, recordID string) string
	}{
		{
			name: "manifest permission denied",
			setup: func(t *testing.T, store *PayloadStore, recordID string) string {
				t.Helper()
				path := filepath.Join(store.payloadDir(recordID), messageFileName)
				if err := os.Chmod(path, 0); err != nil {
					t.Fatalf("Chmod() error: %v", err)
				}
				return path
			},
		},
		{
			name: "attachment permission denied",
			setup: func(t *testing.T, store *PayloadStore, recordID string) string {
				t.Helper()
				path := filepath.Join(store.payloadDir(recordID), attachmentsDirName, "0.bin")
				if err := os.Chmod(path, 0); err != nil {
					t.Fatalf("Chmod() error: %v", err)
				}
				return path
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPayloadTestStore(t)
			saveAttachmentPayload(t, store, recordID)
			path := tc.setup(t, store, recordID)

			_, err := store.Load(recordID)
			if err == nil {
				t.Fatal("expected Load() to fail")
			}
			if _, corrupt := AsPayloadCorruptionError(err); corrupt {
				t.Fatalf("expected generic error, got corruption error: %v", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("expected error to mention %q, got %v", path, err)
			}
		})
	}
}

func TestPayloadStoreMissingPayloadRootReturnsGenericError(t *testing.T) {
	store := newPayloadTestStore(t)
	if err := os.RemoveAll(store.payloadsRoot); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}

	_, err := store.Load(testRecordID(1))
	if err == nil {
		t.Fatal("expected Load() to fail")
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}
	if !strings.Contains(err.Error(), store.payloadsRoot) {
		t.Fatalf("expected error to mention %q, got %v", store.payloadsRoot, err)
	}
}

func TestPayloadStoreSymlinkedPayloadsRootReturnsGenericError(t *testing.T) {
	store := newPayloadTestStore(t)

	externalRoot := filepath.Join(t.TempDir(), payloadsDirName)
	if err := os.MkdirAll(externalRoot, spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.RemoveAll(store.payloadsRoot); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}
	if err := os.Symlink(externalRoot, store.payloadsRoot); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	_, err := store.Load(testRecordID(1))
	if err == nil {
		t.Fatal("expected Load() to fail")
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}
	if !strings.Contains(err.Error(), store.payloadsRoot) {
		t.Fatalf("expected error to mention %q, got %v", store.payloadsRoot, err)
	}
}

func TestPayloadStoreQuarantineOrphansRejectsUnexpectedPayloadEntries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, store *PayloadStore)
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, store *PayloadStore) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(store.payloadsRoot, "unexpected"), []byte("bad"), spoolFileMode); err != nil {
					t.Fatalf("WriteFile() error: %v", err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, store *PayloadStore) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(target, []byte("bad"), spoolFileMode); err != nil {
					t.Fatalf("WriteFile() error: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(store.payloadsRoot, testRecordID(9))); err != nil {
					t.Fatalf("Symlink() error: %v", err)
				}
			},
		},
		{
			name: "invalid id directory",
			setup: func(t *testing.T, store *PayloadStore) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(store.payloadsRoot, "not-a-uuid"), spoolDirMode); err != nil {
					t.Fatalf("MkdirAll() error: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newPayloadTestStore(t)
			tc.setup(t, store)

			_, err := store.QuarantineOrphans(map[string]struct{}{})
			if err == nil {
				t.Fatal("expected QuarantineOrphans() to fail")
			}
			if _, corrupt := AsPayloadCorruptionError(err); corrupt {
				t.Fatalf("expected generic error, got corruption error: %v", err)
			}
			if !strings.Contains(err.Error(), store.payloadsRoot) {
				t.Fatalf("expected error to mention %q, got %v", store.payloadsRoot, err)
			}
		})
	}
}

func TestPayloadStoreQuarantineOrphansSymlinkedPayloadsRootReturnsGenericError(t *testing.T) {
	store := newPayloadTestStore(t)

	externalRoot := filepath.Join(t.TempDir(), payloadsDirName)
	if err := os.MkdirAll(externalRoot, spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.RemoveAll(store.payloadsRoot); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}
	if err := os.Symlink(externalRoot, store.payloadsRoot); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	_, err := store.QuarantineOrphans(map[string]struct{}{})
	if err == nil {
		t.Fatal("expected QuarantineOrphans() to fail")
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}
	if !strings.Contains(err.Error(), store.payloadsRoot) {
		t.Fatalf("expected error to mention %q, got %v", store.payloadsRoot, err)
	}
}

func TestPayloadStoreQuarantineOrphansSymlinkedOrphanRootReturnsGenericError(t *testing.T) {
	store := newPayloadTestStore(t)

	externalRoot := filepath.Join(t.TempDir(), payloadOrphansDirName)
	if err := os.MkdirAll(externalRoot, spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.RemoveAll(store.orphanRoot); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}
	if err := os.Symlink(externalRoot, store.orphanRoot); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	_, err := store.QuarantineOrphans(map[string]struct{}{})
	if err == nil {
		t.Fatal("expected QuarantineOrphans() to fail")
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}
	if !strings.Contains(err.Error(), store.orphanRoot) {
		t.Fatalf("expected error to mention %q, got %v", store.orphanRoot, err)
	}
}

func TestPayloadStoreQuarantineOrphansFallbackWorksForRegularPayload(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	saveAttachmentPayload(t, store, recordID)
	store.renameDirectory = func(sourceParentFD int, sourceName string, targetParentFD int, targetName string) error {
		return unix.EXDEV
	}

	quarantined, err := store.QuarantineOrphans(map[string]struct{}{})
	if err != nil {
		t.Fatalf("QuarantineOrphans() error: %v", err)
	}
	if len(quarantined) != 1 {
		t.Fatalf("unexpected quarantined paths: %#v", quarantined)
	}
	if _, statErr := os.Stat(store.payloadDir(recordID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected payload to be removed from payload root, stat err=%v", statErr)
	}
	if _, err := os.Stat(quarantined[0]); err != nil {
		t.Fatalf("Stat(%q) error: %v", quarantined[0], err)
	}
}

func TestPayloadStoreQuarantineOrphansFallbackRejectsSymlinkedContent(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	saveAttachmentPayload(t, store, recordID)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(store.payloadDir(recordID), "bad-link")); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}
	store.renameDirectory = func(sourceParentFD int, sourceName string, targetParentFD int, targetName string) error {
		return unix.EXDEV
	}

	_, err := store.QuarantineOrphans(map[string]struct{}{})
	if err == nil {
		t.Fatal("expected QuarantineOrphans() to fail")
	}
	if _, corrupt := AsPayloadCorruptionError(err); corrupt {
		t.Fatalf("expected generic error, got corruption error: %v", err)
	}
}

func TestPayloadStoreSymlinkedPayloadDirReturnsCorruptionError(t *testing.T) {
	store := newPayloadTestStore(t)
	targetID := testRecordID(1)
	loadID := testRecordID(2)
	saveAttachmentPayload(t, store, targetID)

	linkPath := store.payloadDir(loadID)
	if err := os.Symlink(store.payloadDir(targetID), linkPath); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	assertPayloadCorruptionPath(t, mustLoadError(t, store, loadID), linkPath)
}

func TestPayloadStoreSymlinkedManifestReturnsCorruptionError(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	saveAttachmentPayload(t, store, recordID)

	manifestPath := filepath.Join(store.payloadDir(recordID), messageFileName)
	externalManifest := filepath.Join(t.TempDir(), "message.json")
	if err := os.WriteFile(externalManifest, []byte(`{"textBody":"symlinked"}`), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if err := os.Symlink(externalManifest, manifestPath); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	assertPayloadCorruptionPath(t, mustLoadError(t, store, recordID), manifestPath)
}

func TestPayloadStoreSymlinkedAttachmentsDirReturnsCorruptionError(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	saveAttachmentPayload(t, store, recordID)

	attachmentsPath := filepath.Join(store.payloadDir(recordID), attachmentsDirName)
	externalDir := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(externalDir, spoolDirMode); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "0.bin"), []byte("hello"), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	if err := os.RemoveAll(attachmentsPath); err != nil {
		t.Fatalf("RemoveAll() error: %v", err)
	}
	if err := os.Symlink(externalDir, attachmentsPath); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	assertPayloadCorruptionPath(t, mustLoadError(t, store, recordID), attachmentsPath)
}

func TestPayloadStoreSymlinkedAttachmentFileReturnsCorruptionError(t *testing.T) {
	store := newPayloadTestStore(t)
	recordID := testRecordID(1)
	saveAttachmentPayload(t, store, recordID)

	attachmentPath := filepath.Join(store.payloadDir(recordID), attachmentsDirName, "0.bin")
	externalFile := filepath.Join(t.TempDir(), "0.bin")
	if err := os.WriteFile(externalFile, []byte("hello"), spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	if err := os.Remove(attachmentPath); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if err := os.Symlink(externalFile, attachmentPath); err != nil {
		t.Fatalf("Symlink() error: %v", err)
	}

	assertPayloadCorruptionPath(t, mustLoadError(t, store, recordID), attachmentPath)
}

func newPayloadTestStore(t *testing.T) *PayloadStore {
	t.Helper()
	store, err := NewPayloadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPayloadStore() error: %v", err)
	}
	return store
}

func saveAttachmentPayload(t *testing.T, store *PayloadStore, id string) {
	t.Helper()
	msg := email.Message{
		To:       []string{"to@example.com"},
		TextBody: "text",
		Attachments: []email.Attachment{
			{Filename: "note.txt", ContentType: "text/plain", Data: []byte("hello")},
		},
	}
	if err := store.Save(id, msg); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
}

func readPayloadManifest(t *testing.T, store *PayloadStore, id string) payloadManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store.payloadDir(id), messageFileName))
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}
	var manifest payloadManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	return manifest
}

func writePayloadManifest(t *testing.T, store *PayloadStore, id string, manifest payloadManifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.payloadDir(id), messageFileName), data, spoolFileMode); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
}

func mustLoadError(t *testing.T, store *PayloadStore, id string) error {
	t.Helper()
	_, err := store.Load(id)
	if err == nil {
		t.Fatal("expected Load() to fail")
	}
	return err
}

func assertPayloadCorruptionPath(t *testing.T, err error, path string) {
	t.Helper()
	var corruptErr *PayloadCorruptionError
	if !errors.As(err, &corruptErr) {
		t.Fatalf("expected PayloadCorruptionError, got %v", err)
	}
	if corruptErr.Path != path {
		t.Fatalf("unexpected corruption path: %q", corruptErr.Path)
	}
}
