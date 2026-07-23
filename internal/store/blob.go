package store

import (
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"lukechampine.com/blake3"
)

// HashBytes returns the content address of the given bytes without storing
// them. It is the single definition of re_gent's object identity, so callers
// that need to verify content they did not write (for example bytes received
// from a server) hash it exactly the way WriteBlob would.
func HashBytes(content []byte) Hash {
	h := blake3.Sum256(content)
	return Hash(hex.EncodeToString(h[:]))
}

// WriteBlob writes content to the object store and returns its hash.
// Content-addressed: identical content produces identical hash.
func (s *Store) WriteBlob(content []byte) (Hash, error) {
	hashStr := string(HashBytes(content))

	// Shard by first 2 hex chars for filesystem efficiency
	dir := filepath.Join(s.Root, "objects", hashStr[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create shard directory: %w", err)
	}

	objPath := filepath.Join(dir, hashStr)

	// If object already exists, we're done (content-addressed deduplication)
	if _, err := os.Stat(objPath); err == nil {
		return Hash(hashStr), nil
	}

	// Write atomically: temp file + rename
	if err := atomicWriteFile(objPath, content, 0o444); err != nil {
		return "", fmt.Errorf("write blob: %w", err)
	}

	return Hash(hashStr), nil
}

// ReadBlob reads a blob from the object store by hash.
// Supports both full 64-char hashes and short hash prefixes.
func (s *Store) ReadBlob(h Hash) ([]byte, error) {
	if len(h) < 2 {
		return nil, fmt.Errorf("invalid hash: too short")
	}

	// Resolve short hash prefix to full hash
	if len(h) < 64 {
		resolved, err := s.ResolveShortHash(string(h))
		if err != nil {
			return nil, err
		}
		h = resolved
	}

	objPath := filepath.Join(s.Root, "objects", string(h[:2]), string(h))
	content, err := os.ReadFile(objPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("blob %s not found", h)
		}
		return nil, fmt.Errorf("read blob %s: %w", h, err)
	}

	return content, nil
}

// atomicWriteFile writes content to path atomically using temp file + rename
func atomicWriteFile(path string, content []byte, writeMode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Ensure cleanup on error
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(content); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	tmpFile = nil // Mark as closed to prevent cleanup

	// On Windows, os.Rename (MoveFileEx with MOVEFILE_REPLACE_EXISTING) refuses to
	// replace a read-only target with "Access is denied". Clearing the read-only bit
	// first lets the rename replace it atomically. We deliberately do NOT os.Remove
	// the target: removing it opens a window where readers (which take no lock) see
	// the ref/blob as missing, breaking the atomic-replacement guarantee on every
	// platform. chmod is ignored on error since the target may not exist yet.
	_ = os.Chmod(path, 0o644)

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	if err := os.Chmod(path, writeMode); err != nil {
		_ = err
	}

	return nil
}

// ObjectExists checks if a blob exists without reading it
func (s *Store) ObjectExists(h Hash) bool {
	if len(h) < 2 {
		return false
	}
	objPath := filepath.Join(s.Root, "objects", string(h[:2]), string(h))
	_, err := os.Stat(objPath)
	return err == nil
}

// WalkObjects walks all objects in the store
func (s *Store) WalkObjects(fn func(Hash) error) error {
	objectsDir := filepath.Join(s.Root, "objects")
	return filepath.WalkDir(objectsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Extract hash from path: objects/ab/abcdef... -> abcdef...
		rel, err := filepath.Rel(objectsDir, path)
		if err != nil || len(rel) < 3 {
			return nil
		}
		// Reconstruct full hash: ab/abcdef... -> ababcdef...
		hash := Hash(filepath.Base(path))
		return fn(hash)
	})
}
