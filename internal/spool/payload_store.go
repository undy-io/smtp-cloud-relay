package spool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/undy-io/smtp-cloud-relay/internal/email"
	"golang.org/x/sys/unix"
)

const (
	payloadsDirName       = "payloads"
	payloadOrphansDirName = "payload-orphans"
	stagingDirName        = "staging"
	publishTempPrefix     = ".publish-"
	messageFileName       = "message.json"
	attachmentsDirName    = "attachments"
)

// PayloadStore persists normalized email payloads and attachment bytes to disk.
type PayloadStore struct {
	root            string
	payloadsRoot    string
	orphanRoot      string
	stagingRoot     string
	renameDirectory func(sourceParentFD int, sourceName string, targetParentFD int, targetName string) error
}

// PayloadCorruptionError identifies a broken spool payload on disk.
type PayloadCorruptionError struct {
	ID   string
	Path string
	Err  error
}

// Error formats the payload corruption message.
func (e *PayloadCorruptionError) Error() string {
	if e == nil {
		return "spool payload corruption"
	}
	if e.Path != "" {
		return fmt.Sprintf("spool payload %q at %s is corrupt: %v", e.ID, e.Path, e.Err)
	}
	return fmt.Sprintf("spool payload %q is corrupt: %v", e.ID, e.Err)
}

// Unwrap returns the underlying payload corruption cause.
func (e *PayloadCorruptionError) Unwrap() error { return e.Err }

// AsPayloadCorruptionError unwraps err into a PayloadCorruptionError.
func AsPayloadCorruptionError(err error) (*PayloadCorruptionError, bool) {
	var target *PayloadCorruptionError
	if !errors.As(err, &target) {
		return nil, false
	}
	return target, true
}

type payloadManifest struct {
	EnvelopeFrom string                 `json:"envelopeFrom,omitempty"`
	HeaderFrom   string                 `json:"headerFrom,omitempty"`
	ReplyTo      []string               `json:"replyTo,omitempty"`
	To           []string               `json:"to,omitempty"`
	Subject      string                 `json:"subject,omitempty"`
	TextBody     string                 `json:"textBody,omitempty"`
	HTMLBody     string                 `json:"htmlBody,omitempty"`
	Attachments  []payloadAttachmentRef `json:"attachments,omitempty"`
}

type payloadAttachmentRef struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Path        string `json:"path"`
	SizeBytes   int64  `json:"sizeBytes"`
	SHA256      string `json:"sha256"`
}

// NewPayloadStore constructs the filesystem payload store rooted at root.
func NewPayloadStore(root string) (*PayloadStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("spool root cannot be empty")
	}
	rootFD, cleanRoot, err := ensureDirectoryPathNoFollow(root)
	if err != nil {
		return nil, fmt.Errorf("ensure spool root %s: %w", root, err)
	}
	defer unixClose(rootFD)

	store := &PayloadStore{
		root:            cleanRoot,
		payloadsRoot:    filepath.Join(cleanRoot, payloadsDirName),
		orphanRoot:      filepath.Join(cleanRoot, payloadOrphansDirName),
		stagingRoot:     filepath.Join(cleanRoot, stagingDirName),
		renameDirectory: renameDirectoryAt,
	}

	payloadsFD, err := ensureDirectoryAt(rootFD, cleanRoot, payloadsDirName)
	if err != nil {
		return nil, err
	}
	defer unixClose(payloadsFD)

	orphanFD, err := ensureDirectoryAt(rootFD, cleanRoot, payloadOrphansDirName)
	if err != nil {
		return nil, err
	}
	defer unixClose(orphanFD)

	stagingFD, err := ensureDirectoryAt(rootFD, cleanRoot, stagingDirName)
	if err != nil {
		return nil, err
	}
	defer unixClose(stagingFD)

	if err := removeTreeContentsFD(stagingFD, store.stagingRoot); err != nil {
		return nil, fmt.Errorf("clean staging root %s: %w", store.stagingRoot, err)
	}
	if err := syncFD(stagingFD, store.stagingRoot); err != nil {
		return nil, err
	}
	if err := store.cleanupPayloadTemps(payloadsFD); err != nil {
		return nil, err
	}

	return store, nil
}

// Save persists the normalized message payload and attachment bytes for id.
func (s *PayloadStore) Save(id string, msg email.Message) error {
	id = strings.TrimSpace(id)
	if err := validateCanonicalRecordID(id); err != nil {
		return err
	}

	rootFD, _, err := openDirectoryPathNoFollow(s.root)
	if err != nil {
		return fmt.Errorf("open spool root %s: %w", s.root, err)
	}
	defer unixClose(rootFD)

	payloadsFD, err := openDirectoryAtNoFollow(rootFD, payloadsDirName)
	if err != nil {
		return fmt.Errorf("open payload root %s: %w", s.payloadsRoot, err)
	}
	defer unixClose(payloadsFD)

	stagingFD, err := openDirectoryAtNoFollow(rootFD, stagingDirName)
	if err != nil {
		return fmt.Errorf("open staging root %s: %w", s.stagingRoot, err)
	}
	defer unixClose(stagingFD)

	mode, exists, err := entryModeAt(payloadsFD, id)
	if err != nil {
		return fmt.Errorf("stat payload target %s: %w", s.payloadDir(id), err)
	}
	if exists {
		if mode != unix.S_IFDIR {
			return unexpectedEntryError(s.payloadsRoot, id)
		}
		return fmt.Errorf("payload already exists: %s", s.payloadDir(id))
	}

	stageName, stageFD, err := createStagingDirectoryAt(stagingFD, s.stagingRoot, id)
	if err != nil {
		return err
	}
	stagePath := filepath.Join(s.stagingRoot, stageName)
	cleanup := true
	defer func() {
		_ = unixClose(stageFD)
		if cleanup {
			_ = removeEntryTreeAt(stagingFD, s.stagingRoot, stageName)
			_ = syncFD(stagingFD, s.stagingRoot)
		}
	}()

	manifest := payloadManifest{
		EnvelopeFrom: strings.TrimSpace(msg.EnvelopeFrom),
		HeaderFrom:   strings.TrimSpace(msg.HeaderFrom),
		ReplyTo:      append([]string(nil), msg.ReplyTo...),
		To:           append([]string(nil), msg.To...),
		Subject:      msg.Subject,
		TextBody:     msg.TextBody,
		HTMLBody:     msg.HTMLBody,
	}

	if len(msg.Attachments) > 0 {
		attachmentsFD, err := createDirectoryAt(stageFD, stagePath, attachmentsDirName)
		if err != nil {
			return err
		}
		attachmentsPath := filepath.Join(stagePath, attachmentsDirName)
		for i, attachment := range msg.Attachments {
			fileName := fmt.Sprintf("%d.bin", i)
			relPath := filepath.ToSlash(filepath.Join(attachmentsDirName, fileName))
			sum := sha256.Sum256(attachment.Data)
			if err := writeRegularFileAt(attachmentsFD, attachmentsPath, fileName, attachment.Data); err != nil {
				_ = unixClose(attachmentsFD)
				return err
			}
			manifest.Attachments = append(manifest.Attachments, payloadAttachmentRef{
				Filename:    attachment.Filename,
				ContentType: attachment.ContentType,
				Path:        relPath,
				SizeBytes:   int64(len(attachment.Data)),
				SHA256:      hex.EncodeToString(sum[:]),
			})
		}
		if err := syncFD(attachmentsFD, attachmentsPath); err != nil {
			_ = unixClose(attachmentsFD)
			return err
		}
		if err := unixClose(attachmentsFD); err != nil {
			return err
		}
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal payload manifest %q: %w", id, err)
	}
	if err := writeRegularFileAt(stageFD, stagePath, messageFileName, data); err != nil {
		return err
	}
	if err := syncFD(stageFD, stagePath); err != nil {
		return err
	}
	if err := s.renameDirectory(stagingFD, stageName, payloadsFD, id); err != nil {
		if !isCrossDeviceError(err) {
			return fmt.Errorf("publish payload %s -> %s: %w", stagePath, s.payloadDir(id), err)
		}
		if err := s.publishViaPayloadTemp(stagingFD, stageName, payloadsFD, id); err != nil {
			return fmt.Errorf("publish payload %s -> %s: %w", stagePath, s.payloadDir(id), err)
		}
		cleanup = false
		return nil
	}
	if err := syncFD(payloadsFD, s.payloadsRoot); err != nil {
		return err
	}
	if err := syncFD(stagingFD, s.stagingRoot); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// Load reconstructs a normalized message payload for id from disk.
func (s *PayloadStore) Load(id string) (email.Message, error) {
	id = strings.TrimSpace(id)
	if err := validateCanonicalRecordID(id); err != nil {
		return email.Message{}, err
	}

	rootFD, _, err := openDirectoryPathNoFollow(s.root)
	if err != nil {
		return email.Message{}, fmt.Errorf("open spool root %s: %w", s.root, err)
	}
	defer unixClose(rootFD)

	payloadsFD, err := openDirectoryAtNoFollow(rootFD, payloadsDirName)
	if err != nil {
		return email.Message{}, fmt.Errorf("open payload root %s: %w", s.payloadsRoot, err)
	}
	defer unixClose(payloadsFD)

	dirPath := s.payloadDir(id)
	dirFD, err := openDirectoryAtNoFollow(payloadsFD, id)
	if err != nil {
		return email.Message{}, classifyRecordPathError(id, dirPath, err, "payload directory is missing")
	}
	defer unixClose(dirFD)

	manifestPath := filepath.Join(dirPath, messageFileName)
	data, err := readRegularFileAtNoFollow(dirFD, messageFileName)
	if err != nil {
		return email.Message{}, classifyRecordPathError(id, manifestPath, err, "payload manifest is missing")
	}

	var manifest payloadManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return email.Message{}, &PayloadCorruptionError{ID: id, Path: manifestPath, Err: fmt.Errorf("decode payload manifest: %w", err)}
	}

	msg := email.Message{
		EnvelopeFrom: strings.TrimSpace(manifest.EnvelopeFrom),
		HeaderFrom:   strings.TrimSpace(manifest.HeaderFrom),
		ReplyTo:      append([]string(nil), manifest.ReplyTo...),
		To:           append([]string(nil), manifest.To...),
		Subject:      manifest.Subject,
		TextBody:     manifest.TextBody,
		HTMLBody:     manifest.HTMLBody,
	}

	var attachmentsFD int = -1
	if len(manifest.Attachments) > 0 {
		attachmentsPath := filepath.Join(dirPath, attachmentsDirName)
		attachmentsFD, err = openDirectoryAtNoFollow(dirFD, attachmentsDirName)
		if err != nil {
			return email.Message{}, classifyRecordPathError(id, attachmentsPath, err, "attachments directory is missing")
		}
		defer unixClose(attachmentsFD)
	}

	for i, ref := range manifest.Attachments {
		fileName, err := validateAttachmentRef(i, ref)
		if err != nil {
			return email.Message{}, &PayloadCorruptionError{ID: id, Path: manifestPath, Err: err}
		}

		fullPath := filepath.Join(dirPath, attachmentsDirName, fileName)
		attachmentBytes, err := readRegularFileAtNoFollow(attachmentsFD, fileName)
		if err != nil {
			return email.Message{}, classifyRecordPathError(id, fullPath, err, "attachment file is missing")
		}
		if int64(len(attachmentBytes)) != ref.SizeBytes {
			return email.Message{}, &PayloadCorruptionError{ID: id, Path: fullPath, Err: fmt.Errorf("attachment size mismatch")}
		}
		sum := sha256.Sum256(attachmentBytes)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(ref.SHA256)) {
			return email.Message{}, &PayloadCorruptionError{ID: id, Path: fullPath, Err: fmt.Errorf("attachment sha256 mismatch")}
		}

		msg.Attachments = append(msg.Attachments, email.Attachment{
			Filename:    ref.Filename,
			ContentType: ref.ContentType,
			Data:        attachmentBytes,
		})
	}

	return msg, nil
}

// Remove deletes the persisted payload for id.
func (s *PayloadStore) Remove(id string) error {
	id = strings.TrimSpace(id)
	if err := validateCanonicalRecordID(id); err != nil {
		return err
	}

	rootFD, _, err := openDirectoryPathNoFollow(s.root)
	if err != nil {
		return fmt.Errorf("open spool root %s: %w", s.root, err)
	}
	defer unixClose(rootFD)

	payloadsFD, err := openDirectoryAtNoFollow(rootFD, payloadsDirName)
	if err != nil {
		return fmt.Errorf("open payload root %s: %w", s.payloadsRoot, err)
	}
	defer unixClose(payloadsFD)

	if err := removeEntryTreeAt(payloadsFD, s.payloadsRoot, id); err != nil {
		return err
	}
	return syncFD(payloadsFD, s.payloadsRoot)
}

// QuarantineOrphans moves payload directories without matching records aside.
func (s *PayloadStore) QuarantineOrphans(validIDs map[string]struct{}) ([]string, error) {
	rootFD, _, err := openDirectoryPathNoFollow(s.root)
	if err != nil {
		return nil, fmt.Errorf("open spool root %s: %w", s.root, err)
	}
	defer unixClose(rootFD)

	payloadsFD, err := openDirectoryAtNoFollow(rootFD, payloadsDirName)
	if err != nil {
		return nil, fmt.Errorf("open payload root %s: %w", s.payloadsRoot, err)
	}
	defer unixClose(payloadsFD)

	orphanFD, err := openDirectoryAtNoFollow(rootFD, payloadOrphansDirName)
	if err != nil {
		return nil, fmt.Errorf("open orphan root %s: %w", s.orphanRoot, err)
	}
	defer unixClose(orphanFD)

	entries, err := readDirectoryEntriesFD(payloadsFD, payloadsDirName)
	if err != nil {
		return nil, fmt.Errorf("read payload root %s: %w", s.payloadsRoot, err)
	}

	var quarantined []string
	for _, entry := range entries {
		name := entry.Name()
		if err := validateCanonicalRecordID(name); err != nil {
			return quarantined, fmt.Errorf("invalid payload entry %s: %w", filepath.Join(s.payloadsRoot, name), err)
		}
		mode, exists, err := entryModeAt(payloadsFD, name)
		if err != nil {
			return quarantined, fmt.Errorf("stat payload entry %s: %w", filepath.Join(s.payloadsRoot, name), err)
		}
		if !exists {
			return quarantined, fmt.Errorf("payload entry disappeared during orphan scan: %s", filepath.Join(s.payloadsRoot, name))
		}
		if mode != unix.S_IFDIR {
			return quarantined, unexpectedEntryError(s.payloadsRoot, name)
		}
		if _, ok := validIDs[name]; ok {
			continue
		}

		targetName, err := uniqueQuarantineNameAt(orphanFD, name)
		if err != nil {
			return quarantined, fmt.Errorf("select quarantine name for %s: %w", filepath.Join(s.payloadsRoot, name), err)
		}
		if err := s.renameDirectory(payloadsFD, name, orphanFD, targetName); err != nil {
			if !isCrossDeviceError(err) {
				return quarantined, fmt.Errorf("move orphan payload %s -> %s: %w", filepath.Join(s.payloadsRoot, name), filepath.Join(s.orphanRoot, targetName), err)
			}
			if err := s.copyAndRemoveOrphan(payloadsFD, name, orphanFD, targetName); err != nil {
				return quarantined, fmt.Errorf("move orphan payload %s -> %s: %w", filepath.Join(s.payloadsRoot, name), filepath.Join(s.orphanRoot, targetName), err)
			}
		}
		if err := syncFD(payloadsFD, s.payloadsRoot); err != nil {
			return quarantined, err
		}
		if err := syncFD(orphanFD, s.orphanRoot); err != nil {
			return quarantined, err
		}
		quarantined = append(quarantined, filepath.Join(s.orphanRoot, targetName))
	}

	return quarantined, nil
}

func (s *PayloadStore) cleanupPayloadTemps(payloadsFD int) error {
	entries, err := readDirectoryEntriesFD(payloadsFD, s.payloadsRoot)
	if err != nil {
		return fmt.Errorf("read payload root %s: %w", s.payloadsRoot, err)
	}
	cleaned := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), publishTempPrefix) {
			continue
		}
		if err := removeEntryTreeAt(payloadsFD, s.payloadsRoot, entry.Name()); err != nil {
			return fmt.Errorf("clean payload publish temp %s: %w", filepath.Join(s.payloadsRoot, entry.Name()), err)
		}
		cleaned = true
	}
	if cleaned {
		if err := syncFD(payloadsFD, s.payloadsRoot); err != nil {
			return err
		}
	}
	return nil
}

func (s *PayloadStore) publishViaPayloadTemp(stagingFD int, stageName string, payloadsFD int, id string) error {
	tempName, tempFD, err := createPublishTempDirectoryAt(payloadsFD, s.payloadsRoot, id)
	if err != nil {
		return err
	}
	_ = unixClose(tempFD)

	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = removeEntryTreeAt(payloadsFD, s.payloadsRoot, tempName)
			_ = syncFD(payloadsFD, s.payloadsRoot)
		}
	}()

	if err := copyDirectoryContentsAt(stagingFD, filepath.Join(s.stagingRoot, stageName), stageName, payloadsFD, filepath.Join(s.payloadsRoot, tempName), tempName); err != nil {
		return err
	}
	if err := s.renameDirectory(payloadsFD, tempName, payloadsFD, id); err != nil {
		return err
	}
	if err := syncFD(payloadsFD, s.payloadsRoot); err != nil {
		return err
	}
	cleanupTemp = false
	_ = removeEntryTreeAt(stagingFD, s.stagingRoot, stageName)
	_ = syncFD(stagingFD, s.stagingRoot)
	return nil
}

func (s *PayloadStore) copyAndRemoveOrphan(payloadsFD int, sourceName string, orphanFD int, targetName string) error {
	if err := copyDirectoryTreeAt(payloadsFD, s.payloadsRoot, sourceName, orphanFD, s.orphanRoot, targetName); err != nil {
		return err
	}
	if err := removeEntryTreeAt(payloadsFD, s.payloadsRoot, sourceName); err != nil {
		_ = removeEntryTreeAt(orphanFD, s.orphanRoot, targetName)
		_ = syncFD(orphanFD, s.orphanRoot)
		return err
	}
	return nil
}

func (s *PayloadStore) payloadDir(id string) string {
	return filepath.Join(s.payloadsRoot, id)
}

func validateAttachmentRef(index int, ref payloadAttachmentRef) (string, error) {
	expectedName := fmt.Sprintf("%d.bin", index)
	expectedPath := filepath.ToSlash(filepath.Join(attachmentsDirName, expectedName))
	if strings.TrimSpace(ref.Path) != expectedPath {
		return "", fmt.Errorf("attachment path must be %q", expectedPath)
	}
	if ref.SizeBytes < 0 {
		return "", fmt.Errorf("attachment size must be non-negative")
	}
	sum, err := hex.DecodeString(strings.TrimSpace(ref.SHA256))
	if err != nil || len(sum) != sha256.Size {
		return "", fmt.Errorf("attachment sha256 must be a 32-byte hex digest")
	}
	return expectedName, nil
}

func unixClose(fd int) error {
	if fd < 0 {
		return nil
	}
	if err := unix.Close(fd); err != nil {
		return fmt.Errorf("close file descriptor: %w", err)
	}
	return nil
}
