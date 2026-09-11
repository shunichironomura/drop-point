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
	SubmissionID string                     `json:"submission_id"`
	Status       droppoint.SubmissionStatus `json:"status"`
}

func HandleSubmitDrop(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Repository == nil || deps.BlobStore == nil {
			writeError(w, http.StatusServiceUnavailable, "drop_storage_unavailable", "drop storage is unavailable")
			return
		}
		submissionID := r.PathValue("submission_id")
		if !token.ValidSubmissionID(submissionID) {
			writeError(w, http.StatusNotFound, "submission_not_found", "submission not found")
			return
		}
		now := deps.Now().UTC()
		dp, err := deps.Repository.FindOpenDropPointByDropTokenHash(r.Context(), token.HashSecret(r.PathValue("drop_token")), now)
		if err != nil {
			writeDropAuthError(w, err)
			return
		}
		requestLimit, err := dropRequestSizeLimit(dp.MaxBytes)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "drop_storage_failed", "drop point has an invalid storage limit")
			return
		}
		if err := deps.Repository.BeginSubmission(r.Context(), dp.ID, submissionID, now); err != nil {
			if errors.Is(err, droppoint.ErrSubmissionAlreadyExists) {
				submitExistingSubmission(w, r, deps, dp.ID, submissionID)
				return
			}
			writeDropAuthError(w, err)
			return
		}

		fail := func(operationErr error, storage bool) {
			cleanupErr := finalizeSubmissionAttempt(r.Context(), deps, dp.ID, submissionID)
			if deps.Logger != nil {
				deps.Logger.Printf("event=submission.failed drop_point_id=%s submission_id=%s error=%q cleanup_error=%q", dp.ID, submissionID, errorMessage(operationErr), errorMessage(cleanupErr))
			}
			if cleanupErr != nil {
				writeStorageFailure(w, cleanupErr)
				return
			}
			if storage {
				writeSubmissionStorageFailure(w, operationErr)
			} else {
				writeMultipartDropError(w, operationErr)
			}
		}

		r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
		envelope, payload, err := multipartDropParts(r)
		if err != nil {
			fail(err, false)
			return
		}
		if _, err := cryptoenv.ValidateEnvelopeJSON(envelope); err != nil {
			fail(err, false)
			return
		}
		stored, err := deps.BlobStore.WriteSubmission(r.Context(), dp.ID, submissionID, envelope, payload, dp.MaxBytes)
		if err != nil {
			fail(err, true)
			return
		}

		commitCtx, cancel := dropFinalizationContext(r.Context())
		commitErr := deps.Repository.CommitSubmission(commitCtx, dp.ID, submissionID, stored, deps.Now().UTC())
		cancel()
		if commitErr != nil {
			verifyCtx, verifyCancel := dropFinalizationContext(r.Context())
			existing, findErr := deps.Repository.FindSubmission(verifyCtx, dp.ID, submissionID)
			verifyCancel()
			if findErr == nil && (existing.Status == droppoint.SubmissionStatusReady || existing.Status == droppoint.SubmissionStatusAcknowledged) {
				writeJSON(w, http.StatusOK, submitDropResponse{SubmissionID: submissionID, Status: existing.Status})
				return
			}
			if findErr != nil && !errors.Is(findErr, droppoint.ErrSubmissionNotFound) {
				if deps.Logger != nil {
					deps.Logger.Printf("event=submission.commit_ambiguous drop_point_id=%s submission_id=%s error=%q verification_error=%q", dp.ID, submissionID, errorMessage(commitErr), errorMessage(findErr))
				}
				writeStorageFailure(w, findErr)
				return
			}
			if findErr == nil && existing.Status != droppoint.SubmissionStatusReceiving {
				writeSubmissionUnavailable(w, submissionStatusError(existing.Status))
				return
			}
			cleanupErr := finalizeSubmissionAttempt(r.Context(), deps, dp.ID, submissionID)
			if cleanupErr != nil {
				writeStorageFailure(w, cleanupErr)
				return
			}
			if errors.Is(commitErr, droppoint.ErrPendingBytesQuotaExceeded) {
				writeError(w, http.StatusTooManyRequests, "submission_queue_full", "drop point submission queue is full")
				return
			}
			writeDropAuthError(w, commitErr)
			return
		}
		writeJSON(w, http.StatusOK, submitDropResponse{SubmissionID: submissionID, Status: droppoint.SubmissionStatusReady})
	}
}

func submitExistingSubmission(w http.ResponseWriter, r *http.Request, deps Dependencies, dropPointID, submissionID string) {
	submission, err := deps.Repository.FindSubmission(r.Context(), dropPointID, submissionID)
	if err != nil {
		writeSubmissionUnavailable(w, err)
		return
	}
	switch submission.Status {
	case droppoint.SubmissionStatusReady, droppoint.SubmissionStatusAcknowledged:
		writeJSON(w, http.StatusOK, submitDropResponse{SubmissionID: submissionID, Status: submission.Status})
	case droppoint.SubmissionStatusReceiving:
		writeError(w, http.StatusConflict, "submission_receiving", "submission is already being received")
	case droppoint.SubmissionStatusFailed:
		writeError(w, http.StatusGone, "submission_failed", "submission is unavailable")
	default:
		writeError(w, http.StatusConflict, "submission_unavailable", "submission is unavailable")
	}
}

func finalizeSubmissionAttempt(parent context.Context, deps Dependencies, dropPointID, submissionID string) error {
	ctx, cancel := dropFinalizationContext(parent)
	defer cancel()
	if err := deps.BlobStore.DeleteSubmission(ctx, dropPointID, submissionID); err != nil {
		return err
	}
	return deps.Repository.DeleteReceivingSubmission(ctx, dropPointID, submissionID)
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

func writeSubmissionStorageFailure(w http.ResponseWriter, err error) {
	switch {
	case dropRequestTooLarge(err):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "encrypted payload exceeds the drop point limit")
	case blobstore.ClassifyFailure(err) == blobstore.FailureClientInput:
		writeError(w, http.StatusBadRequest, "drop_multipart_invalid", "could not read the encrypted payload")
	default:
		writeStorageFailure(w, err)
	}
}

func dropRequestTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.Is(err, droppoint.ErrPayloadTooLarge) || errors.As(err, &maxBytesErr)
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
	return envelope, &multipartPayloadReader{part: payloadPart, reader: reader}, nil
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
	case errors.Is(err, droppoint.ErrDropPointExpired), errors.Is(err, droppoint.ErrDropPointClosed), errors.Is(err, droppoint.ErrDropPointFailed):
		writeError(w, http.StatusGone, "drop_point_unavailable", "drop point is unavailable")
	case errors.Is(err, droppoint.ErrPendingSubmissionQuotaExceeded), errors.Is(err, droppoint.ErrPendingBytesQuotaExceeded):
		writeError(w, http.StatusTooManyRequests, "submission_queue_full", "drop point submission queue is full")
	case errors.Is(err, droppoint.ErrSubmissionAlreadyExists), errors.Is(err, droppoint.ErrDropPointNotOpen):
		writeError(w, http.StatusConflict, "submission_unavailable", "submission cannot be accepted")
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
