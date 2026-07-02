package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/shunichironomura/drop-point/internal/cryptoenv"
	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/token"
)

const (
	maxEnvelopeBytes        = 1 << 20
	multipartOverhead       = 64 << 10
	envelopePartName        = "envelope"
	payloadPartName         = "payload"
	jsonContentType         = "application/json"
	octetContentType        = "application/octet-stream"
	multipartFormPrefix     = "multipart/form-data"
	dropFinalizationTimeout = 10 * time.Second
)

type submitDropResponse struct {
	Status droppoint.Status `json:"status"`
}

// HandleSubmitDrop handles PUT /api/drops/:drop_token.
func HandleSubmitDrop(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Repository == nil || deps.BlobStore == nil {
			writeError(w, http.StatusServiceUnavailable, "drop_storage_unavailable", "drop storage is unavailable")
			return
		}
		dropToken := r.PathValue("drop_token")
		now := deps.Now().UTC()
		dp, err := deps.Repository.FindOpenDropPointByDropTokenHash(r.Context(), token.HashSecret(dropToken), now)
		if err != nil {
			writeDropAuthError(w, err)
			return
		}
		if err := deps.Repository.BeginReceivingDrop(r.Context(), dp.ID, now); err != nil {
			writeDropAuthError(w, err)
			return
		}

		committed := false
		defer func() {
			if !committed {
				cleanupCtx, cancel := dropFinalizationContext(r.Context())
				defer cancel()
				_ = deps.BlobStore.DeleteDropPoint(cleanupCtx, dp.ID)
				_ = deps.Repository.ResetReceivingDrop(cleanupCtx, dp.ID, deps.Now().UTC())
			}
		}()

		r.Body = http.MaxBytesReader(w, r.Body, dp.MaxBytes+maxEnvelopeBytes+multipartOverhead)
		envelope, payload, err := multipartDropParts(r)
		if err != nil {
			writeMultipartDropError(w, err)
			return
		}
		if _, err := cryptoenv.ValidateEnvelopeJSON(envelope); err != nil {
			writeMultipartDropError(w, err)
			return
		}

		result, err := deps.BlobStore.WriteDrop(r.Context(), dp.ID, envelope, payload, dp.MaxBytes)
		if err != nil {
			writeMultipartDropError(w, err)
			return
		}
		commitCtx, cancel := dropFinalizationContext(r.Context())
		commitErr := deps.Repository.CommitReceivedDrop(commitCtx, dp.ID, result, deps.Now().UTC())
		cancel()
		if commitErr != nil {
			writeDropAuthError(w, commitErr)
			return
		}
		committed = true
		writeJSON(w, http.StatusOK, submitDropResponse{Status: droppoint.StatusReady})
	}
}

func dropFinalizationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), dropFinalizationTimeout)
}

func multipartDropParts(r *http.Request) ([]byte, io.Reader, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != multipartFormPrefix {
		return nil, nil, fmt.Errorf("multipart content type required")
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, nil, fmt.Errorf("multipart boundary required")
	}
	reader := multipart.NewReader(r.Body, boundary)

	envelopePart, err := reader.NextPart()
	if err != nil {
		return nil, nil, fmt.Errorf("missing envelope part: %w", err)
	}
	defer envelopePart.Close()
	if err := validatePart(envelopePart, envelopePartName, jsonContentType); err != nil {
		return nil, nil, err
	}
	envelope, err := readEnvelopePart(envelopePart)
	if err != nil {
		return nil, nil, err
	}

	payloadPart, err := reader.NextPart()
	if err != nil {
		return nil, nil, fmt.Errorf("missing payload part: %w", err)
	}
	if err := validatePart(payloadPart, payloadPartName, octetContentType); err != nil {
		_ = payloadPart.Close()
		return nil, nil, err
	}

	payloadReader := &multipartPayloadReader{part: payloadPart, reader: reader}
	return envelope, payloadReader, nil
}

func validatePart(part *multipart.Part, wantName string, wantContentType string) error {
	if part.FormName() != wantName {
		return fmt.Errorf("multipart part %q must be %q", part.FormName(), wantName)
	}
	mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || mediaType != wantContentType {
		return fmt.Errorf("multipart part %q must use content type %s", wantName, wantContentType)
	}
	return nil
}

func readEnvelopePart(part io.Reader) ([]byte, error) {
	limited := io.LimitReader(part, maxEnvelopeBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read envelope part: %w", err)
	}
	if len(data) > maxEnvelopeBytes {
		return nil, fmt.Errorf("envelope part too large")
	}
	return data, nil
}

func writeDropAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, droppoint.ErrDropTokenInvalid), errors.Is(err, droppoint.ErrDropPointNotFound):
		writeError(w, http.StatusNotFound, "drop_point_not_found", "drop point not found")
	case errors.Is(err, droppoint.ErrDropPointExpired), errors.Is(err, droppoint.ErrDropPointClosed):
		writeError(w, http.StatusGone, "drop_point_unavailable", "drop point is unavailable")
	case errors.Is(err, droppoint.ErrDropAlreadyExists), errors.Is(err, droppoint.ErrDropPointNotOpen):
		writeError(w, http.StatusConflict, "drop_already_exists", "drop point cannot accept another drop")
	default:
		writeError(w, http.StatusInternalServerError, "drop_failed", "could not complete drop")
	}
}

func writeMultipartDropError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.Is(err, droppoint.ErrPayloadTooLarge), errors.As(err, &maxBytesErr):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "encrypted payload exceeds the drop point limit")
	case errors.Is(err, droppoint.ErrEnvelopeInvalid):
		writeError(w, http.StatusBadRequest, "envelope_invalid", "envelope is invalid")
	default:
		writeError(w, http.StatusBadRequest, "drop_multipart_invalid", "drop must contain envelope JSON and payload octet-stream parts")
	}
}

type multipartPayloadReader struct {
	part    *multipart.Part
	reader  *multipart.Reader
	checked bool
}

func (r *multipartPayloadReader) Read(p []byte) (int, error) {
	n, err := r.part.Read(p)
	if !errors.Is(err, io.EOF) || r.checked {
		return n, err
	}
	r.checked = true
	if closeErr := r.part.Close(); closeErr != nil {
		return n, closeErr
	}
	extra, nextErr := r.reader.NextPart()
	switch {
	case errors.Is(nextErr, io.EOF):
		return n, io.EOF
	case nextErr != nil:
		return n, nextErr
	default:
		_ = extra.Close()
		return n, fmt.Errorf("unexpected extra multipart part %q", extra.FormName())
	}
}
