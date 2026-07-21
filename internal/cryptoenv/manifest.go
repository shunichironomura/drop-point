package cryptoenv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
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

const (
	MaxManifestFiles          = 1000
	MaxFilenameUTF8Bytes      = 240
	MaxFilenameExtensionBytes = 32
	MaxMIMETypeUTF8Bytes      = 255
	reservedReceiptName       = ".droppoint-receipt.json"
)

var safeMIMEPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*$`)

func BuildManifest(files []PlainFile, createdAt time.Time) (Manifest, []byte, error) {
	if len(files) == 0 {
		return Manifest{}, nil, fmt.Errorf("bundle must contain at least one file")
	}
	if len(files) > MaxManifestFiles {
		return Manifest{}, nil, fmt.Errorf("bundle contains %d files, maximum is %d", len(files), MaxManifestFiles)
	}
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.Name
	}
	canonicalNames, err := CanonicalizeFilenames(names)
	if err != nil {
		return Manifest{}, nil, err
	}
	manifest := Manifest{ProtocolVersion: ProtocolVersion, CreatedAt: createdAt.UTC().Format(time.RFC3339Nano)}
	var payload bytes.Buffer
	for i, file := range files {
		mediaType, err := SanitizeMIMEType(file.Type)
		if err != nil {
			return Manifest{}, nil, fmt.Errorf("file %d MIME type invalid: %w", i, err)
		}
		manifest.Files = append(manifest.Files, ManifestFile{Name: canonicalNames[i], Type: mediaType, Size: int64(len(file.Data))})
		_, _ = payload.Write(file.Data)
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
	if len(manifest.Files) > MaxManifestFiles {
		return fmt.Errorf("manifest contains %d files, maximum is %d", len(manifest.Files), MaxManifestFiles)
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
		if _, ok := seen[filenameCollisionKey(safeName)]; ok {
			return fmt.Errorf("manifest file %d duplicates filename %q", i, safeName)
		}
		seen[filenameCollisionKey(safeName)] = struct{}{}
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

// CanonicalizeFilenames applies the sender policy and deterministically adds
// " (n)" before the extension when normalized comparison keys collide.
func CanonicalizeFilenames(names []string) ([]string, error) {
	if len(names) > MaxManifestFiles {
		return nil, fmt.Errorf("bundle contains %d files, maximum is %d", len(names), MaxManifestFiles)
	}
	used := make(map[string]struct{}, len(names))
	canonical := make([]string, 0, len(names))
	for i, name := range names {
		base, err := canonicalizeFilename(name)
		if err != nil {
			return nil, fmt.Errorf("file %d name invalid: %w", i, err)
		}
		candidate := base
		for suffix := 2; ; suffix++ {
			key := filenameCollisionKey(candidate)
			if _, exists := used[key]; !exists {
				used[key] = struct{}{}
				canonical = append(canonical, candidate)
				break
			}
			candidate = fitFilename(base, fmt.Sprintf(" (%d)", suffix))
		}
	}
	return canonical, nil
}

// SanitizeFilename validates a receiver-authenticated manifest name. Receivers
// reject non-canonical names rather than rewriting them differently.
func SanitizeFilename(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("filename must be valid UTF-8")
	}
	if name == "" {
		return "", fmt.Errorf("filename must not be empty")
	}
	if len(name) > MaxFilenameUTF8Bytes {
		return "", fmt.Errorf("filename exceeds %d UTF-8 bytes", MaxFilenameUTF8Bytes)
	}
	if !norm.NFC.IsNormalString(name) {
		return "", fmt.Errorf("filename must use Unicode NFC")
	}
	if strings.TrimRight(name, " .") != name {
		return "", fmt.Errorf("filename must not end in a space or dot")
	}
	if filenameBlank(name) || name == "." || name == ".." {
		return "", fmt.Errorf("filename is blank or reserved")
	}
	for _, r := range name {
		if forbiddenFilenameRune(r) {
			return "", fmt.Errorf("filename contains a platform-invalid character")
		}
	}
	if reservedWindowsName(name) || strings.EqualFold(name, reservedReceiptName) {
		return "", fmt.Errorf("filename is platform-reserved")
	}
	return name, nil
}

func SanitizeMIMEType(value string) (string, error) {
	if value == "" {
		return "application/octet-stream", nil
	}
	if !utf8.ValidString(value) || len(value) > MaxMIMETypeUTF8Bytes {
		return "", fmt.Errorf("MIME type must be valid UTF-8 and at most %d bytes", MaxMIMETypeUTF8Bytes)
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

func canonicalizeFilename(name string) (string, error) {
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("filename must be valid UTF-8")
	}
	normalized := norm.NFC.String(name)
	var builder strings.Builder
	builder.Grow(len(normalized))
	for _, r := range normalized {
		if forbiddenFilenameRune(r) {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(r)
	}
	candidate := strings.TrimRight(builder.String(), " .")
	if filenameBlank(candidate) || candidate == "." || candidate == ".." {
		candidate = "file"
	}
	if reservedWindowsName(candidate) || strings.EqualFold(candidate, reservedReceiptName) {
		candidate = "_" + candidate
	}
	candidate = fitFilename(candidate, "")
	if _, err := SanitizeFilename(candidate); err != nil {
		return "", fmt.Errorf("could not canonicalize filename: %w", err)
	}
	return candidate, nil
}

func fitFilename(base string, suffix string) string {
	stem, extension := base, ""
	if dot := strings.LastIndex(base, "."); dot > 0 {
		possibleExtension := base[dot:]
		if len(possibleExtension) <= MaxFilenameExtensionBytes {
			stem, extension = base[:dot], possibleExtension
		}
	}
	budget := MaxFilenameUTF8Bytes - len(suffix) - len(extension)
	if budget < 1 {
		extension = ""
		budget = MaxFilenameUTF8Bytes - len(suffix)
	}
	stem = strings.TrimRight(truncateUTF8(stem, budget), " .")
	if filenameBlank(stem) || stem == "." || stem == ".." {
		stem = "file"
	}
	return stem + suffix + extension
}

func truncateUTF8(value string, maxBytes int) string {
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func forbiddenFilenameRune(r rune) bool {
	return r == 0 || r == '/' || r == '\\' || strings.ContainsRune(`<>:"|?*`, r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf)
}

func filenameBlank(name string) bool {
	if name == "" {
		return true
	}
	for _, r := range name {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func filenameCollisionKey(name string) string {
	return strings.ToLower(norm.NFC.String(name))
}

func reservedWindowsName(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "COM¹", "COM²", "COM³",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "LPT¹", "LPT²", "LPT³":
		return true
	default:
		return false
	}
}
