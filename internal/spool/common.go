package spool

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	spoolDirMode  = 0o700
	spoolFileMode = 0o600
)

func isValidState(state State) bool {
	for _, candidate := range allStates {
		if state == candidate {
			return true
		}
	}
	return false
}

func normalizeLastError(lastErr *LastError, fallback time.Time) *LastError {
	if lastErr == nil {
		return nil
	}

	cloned := *lastErr
	cloned.Message = strings.TrimSpace(cloned.Message)
	cloned.Provider = strings.TrimSpace(cloned.Provider)
	if cloned.Timestamp.IsZero() {
		if !fallback.IsZero() {
			cloned.Timestamp = fallback.UTC()
		}
	} else {
		cloned.Timestamp = cloned.Timestamp.UTC()
	}
	return &cloned
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, spoolDirMode); err != nil {
		return fmt.Errorf("create spool directory %s: %w", path, err)
	}
	if err := os.Chmod(path, spoolDirMode); err != nil {
		return fmt.Errorf("chmod spool directory %s: %w", path, err)
	}
	return syncDir(path)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %s for fsync: %w", path, err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync directory %s: %w", path, err)
	}
	return nil
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:]), nil
}

func validateCanonicalRecordID(id string) error {
	if id == "" {
		return fmt.Errorf("record id cannot be empty")
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("record id %q must not have surrounding whitespace", id)
	}
	if len(id) != 36 {
		return fmt.Errorf("record id %q must be a canonical lowercase UUID", id)
	}
	for i := 0; i < len(id); i++ {
		switch i {
		case 8, 13, 18, 23:
			if id[i] != '-' {
				return fmt.Errorf("record id %q must be a canonical lowercase UUID", id)
			}
		case 14:
			if id[i] != '4' {
				return fmt.Errorf("record id %q must be a canonical lowercase UUID", id)
			}
		case 19:
			if id[i] != '8' && id[i] != '9' && id[i] != 'a' && id[i] != 'b' {
				return fmt.Errorf("record id %q must be a canonical lowercase UUID", id)
			}
		default:
			c := id[i]
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return fmt.Errorf("record id %q must be a canonical lowercase UUID", id)
			}
		}
	}
	return nil
}
