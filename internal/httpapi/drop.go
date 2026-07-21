package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/shunichironomura/droppoint/internal/blobstore"
	"github.com/shunichironomura/droppoint/internal/cryptoenv"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
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

type dropFailureStage uint8

const (
	dropFailureMultipart dropFailureStage = iota
	dropFailureStorage
	dropFailureCommit
)

type dropFinalizationState uint8

const (
	dropFinalizationOpen dropFinalizationState = iota
	dropFinalizationTerminal
	dropFinalizationPending
)

type dropFinalizationResult struct {
	State     dropFinalizationState
	DeleteErr error
	ResetErr  error
}

func (r dropFinalizationResult) Err() error {
	return errors.Join(r.DeleteErr, r.ResetErr)
}

type submitDropFailure struct {
	Stage        dropFailureStage
	OperationErr error
	Finalization dropFinalizationResult
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
		requestLimit, err := dropRequestSizeLimit(dp.MaxBytes)
		if err != nil {
			if deps.Logger != nil {
				deps.Logger.Printf("event=drop.invalid_size_limit drop_point_id=%s error=%q", dp.ID, err)
			}
			writeError(w, http.StatusInternalServerError, "drop_storage_failed", "drop point has an invalid storage limit")
			return
		}
		if err := deps.Repository.BeginReceivingDrop(r.Context(), dp.ID, now); err != nil {
			writeDropAuthError(w, err)
			return
		}

		fail := func(stage dropFailureStage, operationErr error) {
			finalization := finalizeDropAttempt(r.Context(), deps, dp.ID)
			writeSubmitDropFailure(w, deps, dp.ID, submitDropFailure{Stage: stage, OperationErr: operationErr, Finalization: finalization})
		}

		r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
		envelope, payload, err := multipartDropParts(r)
		if err != nil {
			fail(dropFailureMultipart, err)
			return
		}
		if _, err := cryptoenv.ValidateEnvelopeJSON(envelope); err != nil {
			fail(dropFailureMultipart, err)
			return
		}

		result, err := deps.BlobStore.WriteDrop(r.Context(), dp.ID, envelope, payload, dp.MaxBytes)
		if err != nil {
			fail(dropFailureStorage, err)
			return
		}
		commitCtx, cancel := dropFinalizationContext(r.Context())
		commitErr := deps.Repository.CommitReceivedDrop(commitCtx, dp.ID, result, deps.Now().UTC())
		cancel()
		if commitErr != nil {
			fail(dropFailureCommit, commitErr)
			return
		}
		writeJSON(w, http.StatusOK, submitDropResponse{Status: droppoint.StatusReady})
	}
}

func dropRequestSizeLimit(maxPayloadBytes int64) (int64, error) {
	const framingBytes = int64(maxEnvelopeBytes + multipartOverhead)
	if maxPayloadBytes <= 0 || maxPayloadBytes > math.MaxInt64-framingBytes {
		return 0, fmt.Errorf("payload limit %d cannot be combined with framing allowance", maxPayloadBytes)
	}
	return maxPayloadBytes + framingBytes, nil
}

func dropFinalizationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), dropFinalizationTimeout)
}

func finalizeDropAttempt(parent context.Context, deps Dependencies, id string) dropFinalizationResult {
	ctx, cancel := dropFinalizationContext(parent)
	defer cancel()
	if err := deps.BlobStore.DeleteDropPoint(ctx, id); err != nil {
		return dropFinalizationResult{State: dropFinalizationPending, DeleteErr: err}
	}
	if err := deps.Repository.ResetReceivingDrop(ctx, id, deps.Now().UTC()); err != nil {
		if errors.Is(err, droppoint.ErrDropPointExpired) {
			return dropFinalizationResult{State: dropFinalizationTerminal}
		}
		dp, lookupErr := deps.Repository.FindDropPointByID(ctx, id)
		if lookupErr != nil {
			return dropFinalizationResult{State: dropFinalizationPending, ResetErr: errors.Join(err, lookupErr)}
		}
		switch dp.Status {
		case droppoint.StatusOpen:
			return dropFinalizationResult{State: dropFinalizationOpen}
		case droppoint.StatusClosed, droppoint.StatusExpired, droppoint.StatusFailed:
			return dropFinalizationResult{State: dropFinalizationTerminal}
		default:
			return dropFinalizationResult{State: dropFinalizationPending, ResetErr: err}
		}
	}
	return dropFinalizationResult{State: dropFinalizationOpen}
}

func writeSubmitDropFailure(w http.ResponseWriter, deps Dependencies, id string, failure submitDropFailure) {
	finalizationErr := failure.Finalization.Err()
	if deps.Logger != nil && (failure.Stage != dropFailureMultipart || finalizationErr != nil) {
		deps.Logger.Printf(
			"event=drop.failed drop_point_id=%s stage=%d error=%q finalization_state=%d finalization_error=%q",
			id,
			failure.Stage,
			errorMessage(failure.OperationErr),
			failure.Finalization.State,
			errorMessage(finalizationErr),
		)
	}
	if finalizationErr != nil {
		writeStorageFailure(w, finalizationErr)
		return
	}

	switch failure.Stage {
	case dropFailureMultipart:
		writeMultipartDropError(w, failure.OperationErr)
	case dropFailureStorage:
		switch {
		case errors.Is(failure.OperationErr, droppoint.ErrPayloadTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "encrypted payload exceeds the drop point limit")
		case blobstore.ClassifyFailure(failure.OperationErr) == blobstore.FailureClientInput:
			writeError(w, http.StatusBadRequest, "drop_multipart_invalid", "could not read the encrypted payload")
		default:
			writeStorageFailure(w, failure.OperationErr)
		}
	case dropFailureCommit:
		writeDropAuthError(w, failure.OperationErr)
	default:
		writeError(w, http.StatusInternalServerError, "drop_failed", "could not complete drop")
	}
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeStorageFailure(w http.ResponseWriter, err error) {
	switch blobstore.ClassifyFailure(err) {
	case blobstore.FailureCapacity:
		writeError(w, http.StatusInsufficientStorage, "storage_full", "drop storage has insufficient capacity")
	case blobstore.FailureUnavailable:
		writeError(w, http.StatusServiceUnavailable, "drop_storage_unavailable", "drop storage is temporarily unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "drop_storage_failed", "could not durably store the drop")
	}
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
