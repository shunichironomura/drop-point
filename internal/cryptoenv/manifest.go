package cryptoenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type Manifest struct {
	ProtocolVersion int            `json:"protocol_version"`
	Files           []ManifestFile `json:"files"`
	CreatedAt       string         `json:"created_at"`
}

type ManifestFile struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

var safeMIMEPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*$`)

func BuildManifest(files []PlainFile, createdAt time.Time) (Manifest, []byte, error) {
	if len(files) == 0 {
		return Manifest{}, nil, fmt.Errorf("bundle must contain at least one file")
	}
	manifest := Manifest{ProtocolVersion: ProtocolVersion, CreatedAt: createdAt.UTC().Format(time.RFC3339Nano)}
	var payload bytes.Buffer
	for _, file := range files {
		if file.Name == "" {
			return Manifest{}, nil, fmt.Errorf("file name must not be empty")
		}
		manifest.Files = append(manifest.Files, ManifestFile{Name: file.Name, Type: file.Type, Size: int64(len(file.Data))})
		payload.Write(file.Data)
	}
	return manifest, payload.Bytes(), nil
}

func ParseManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest JSON: %w", err)
	}
	var trailing any
	switch err := decoder.Decode(&trailing); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return Manifest{}, fmt.Errorf("manifest JSON must contain one value")
	default:
		return Manifest{}, fmt.Errorf("decode manifest JSON: %w", err)
	}
	return manifest, nil
}

func ValidateManifest(manifest Manifest, payloadPlaintext []byte) error {
	if manifest.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("manifest protocol_version = %d, want %d", manifest.ProtocolVersion, ProtocolVersion)
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("manifest must contain at least one file")
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return fmt.Errorf("manifest created_at is invalid: %w", err)
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	remaining := int64(len(payloadPlaintext))
	for i, file := range manifest.Files {
		if file.Size < 0 {
			return fmt.Errorf("manifest file %d size must be non-negative", i)
		}
		if file.Size > remaining {
			return fmt.Errorf("manifest file %d size %d exceeds remaining payload length %d", i, file.Size, remaining)
		}
		safeName, err := SanitizeFilename(file.Name)
		if err != nil {
			return fmt.Errorf("manifest file %d name invalid: %w", i, err)
		}
		if _, ok := seen[strings.ToLower(safeName)]; ok {
			return fmt.Errorf("manifest file %d duplicates filename %q", i, safeName)
		}
		seen[strings.ToLower(safeName)] = struct{}{}
		if _, err := SanitizeMIMEType(file.Type); err != nil {
			return fmt.Errorf("manifest file %d MIME type invalid: %w", i, err)
		}
		remaining -= file.Size
	}
	if remaining != 0 {
		return fmt.Errorf("manifest size sum = %d, payload length = %d", int64(len(payloadPlaintext))-remaining, len(payloadPlaintext))
	}
	return nil
}

func SplitPayload(manifest Manifest, payloadPlaintext []byte) ([]RecoveredFile, error) {
	if err := ValidateManifest(manifest, payloadPlaintext); err != nil {
		return nil, err
	}
	files := make([]RecoveredFile, 0, len(manifest.Files))
	offset := 0
	for i, entry := range manifest.Files {
		if entry.Size < 0 || entry.Size > int64(len(payloadPlaintext)-offset) {
			return nil, fmt.Errorf("manifest file %d size is outside the remaining payload", i)
		}
		next := offset + int(entry.Size)
		if next < offset || next > len(payloadPlaintext) {
			return nil, fmt.Errorf("manifest file %d payload range is invalid", i)
		}
		safeName, err := SanitizeFilename(entry.Name)
		if err != nil {
			return nil, err
		}
		safeType, err := SanitizeMIMEType(entry.Type)
		if err != nil {
			return nil, err
		}
		files = append(files, RecoveredFile{
			Name:     entry.Name,
			SafeName: safeName,
			Type:     entry.Type,
			SafeType: safeType,
			Data:     append([]byte{}, payloadPlaintext[offset:next]...),
		})
		offset = next
	}
	return files, nil
}

func SanitizeFilename(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("filename must not be empty")
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("filename must be a base name")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("filename is reserved")
	}
	for _, r := range name {
		if r == 0 || unicode.IsControl(r) {
			return "", fmt.Errorf("filename contains control characters")
		}
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("filename must not be blank")
	}
	if reservedWindowsName(trimmed) {
		return "", fmt.Errorf("filename is platform-reserved")
	}
	return trimmed, nil
}

func SanitizeMIMEType(value string) (string, error) {
	if value == "" {
		return "application/octet-stream", nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", err
	}
	mediaType = strings.ToLower(mediaType)
	if !safeMIMEPattern.MatchString(mediaType) {
		return "", fmt.Errorf("unsafe MIME type %q", value)
	}
	return mediaType, nil
}

func reservedWindowsName(name string) bool {
	base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}
