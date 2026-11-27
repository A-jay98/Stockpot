package backup

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ManifestVersion is bumped when the layout changes incompatibly.
const ManifestVersion = 1

// Artifact names.
const (
	ArtifactDB    = "db"    // pg_dump custom format
	ArtifactBlobs = "blobs" // tar of the blob directory
)

// Manifest describes one backup: everything restore needs to find, verify
// and reassemble its parts. It is stored in backup_run and sent to the
// channel as the last message of the run.
type Manifest struct {
	Version       int        `json:"version"`
	RunID         uuid.UUID  `json:"run_id"`
	CreatedAt     time.Time  `json:"created_at"`
	AppVersion    string     `json:"app_version"`
	SchemaVersion int64      `json:"schema_version"`
	AgeRecipient  string     `json:"age_recipient"`
	PartSize      int64      `json:"part_size"`
	Artifacts     []Artifact `json:"artifacts"`
}

// Artifact is one stream (database or blobs): zstd-compressed, age-encrypted,
// split into parts; concatenating the parts in order gives the ciphertext.
type Artifact struct {
	Name  string `json:"name"`
	Parts []Part `json:"parts"`
}

// Part is one uploaded file.
type Part struct {
	Index     int    `json:"index"` // 1-based
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	FileID    string `json:"file_id"`
	MessageID int64  `json:"message_id"`
}

// Size is the total uploaded bytes.
func (m Manifest) Size() (n int64) {
	for _, a := range m.Artifacts {
		for _, p := range a.Parts {
			n += p.Size
		}
	}
	return n
}

// PartCount is the number of uploaded parts.
func (m Manifest) PartCount() (n int) {
	for _, a := range m.Artifacts {
		n += len(a.Parts)
	}
	return n
}

// ParseManifest decodes and sanity-checks a manifest.
func ParseManifest(b []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	if m.Version != ManifestVersion {
		return m, fmt.Errorf("manifest: unsupported version %d", m.Version)
	}
	for _, a := range m.Artifacts {
		for i, p := range a.Parts {
			if p.Index != i+1 || p.SHA256 == "" || p.Filename == "" {
				return m, fmt.Errorf("manifest: bad part %d of %s", i+1, a.Name)
			}
		}
	}
	return m, nil
}

// Artifact returns the named artifact.
func (m Manifest) Artifact(name string) (Artifact, bool) {
	for _, a := range m.Artifacts {
		if a.Name == name {
			return a, true
		}
	}
	return Artifact{}, false
}
