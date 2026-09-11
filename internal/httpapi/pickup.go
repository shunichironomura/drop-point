package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"time"

	"github.com/shunichironomura/droppoint/internal/cryptoenv"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

const pickupFinalizationTimeout = 10 * time.Second

func HandlePickupSubmission(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("drop_point_id")
		dp, ok := authorizePickup(w, r, deps, id)
		if !ok || !requireOpenDropPoint(w, dp) {
			return
		}
		submissionID := r.PathValue("submission_id")
		if !token.ValidSubmissionID(submissionID) {
			writeError(w, http.StatusNotFound, "submission_not_found", "submission not found")
			return
		}
		submission, err := deps.Repository.FindSubmission(r.Context(), id, submissionID)
		if err != nil {
			writeSubmissionUnavailable(w, err)
			return
		}
		if submission.Status != droppoint.SubmissionStatusReady {
			writeSubmissionUnavailable(w, submissionStatusError(submission.Status))
			return
		}
		if submission.EnvelopePath == "" || submission.PayloadPath == "" {
			failCorruptSubmission(r.Context(), deps, id, submissionID, "missing_blob_pointer")
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		if deps.BlobStore == nil {
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		envelope, err := deps.BlobStore.ReadEnvelope(r.Context(), submission.EnvelopePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				failCorruptSubmission(r.Context(), deps, id, submissionID, "missing_envelope_blob")
			}
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored envelope is unavailable")
			return
		}
		if _, err := cryptoenv.ValidateEnvelopeJSON(envelope); err != nil {
			failCorruptSubmission(r.Context(), deps, id, submissionID, "invalid_envelope_blob")
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored envelope is unavailable")
			return
		}
		payload, payloadSize, err := deps.BlobStore.OpenPayload(r.Context(), submission.PayloadPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				failCorruptSubmission(r.Context(), deps, id, submissionID, "missing_payload_blob")
			}
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		defer payload.Close()
		if payloadSize != submission.EncryptedSize {
			failCorruptSubmission(r.Context(), deps, id, submissionID, "payload_size_mismatch")
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		if r.Method == http.MethodHead {
			writer := multipart.NewWriter(io.Discard)
			w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			return
		}

		if err := writePickupMultipart(w, envelope, payload); err != nil {
			if deps.Logger != nil {
				deps.Logger.Printf("event=pickup.response_failed drop_point_id=%s submission_id=%s error=%q", id, submissionID, err)
			}
			return
		}
		finalizeCtx, cancel := pickupFinalizationContext(r.Context())
		defer cancel()
		if err := deps.Repository.MarkSubmissionPickedUp(finalizeCtx, id, submissionID, deps.Now().UTC()); err != nil && deps.Logger != nil {
			deps.Logger.Printf("event=pickup.timestamp_failed drop_point_id=%s submission_id=%s error=%q", id, submissionID, err)
		}
	}
}

func failCorruptSubmission(parent context.Context, deps Dependencies, id, submissionID, reason string) {
	ctx, cancel := pickupFinalizationContext(parent)
	defer cancel()
	if err := deps.Repository.FailSubmission(ctx, id, submissionID, deps.Now().UTC()); err != nil {
		if deps.Logger != nil {
			deps.Logger.Printf("event=submission.fail_transition_failed drop_point_id=%s submission_id=%s reason=%s error=%q", id, submissionID, reason, err)
		}
		return
	}
	if deps.Logger != nil {
		deps.Logger.Printf("event=submission.failed_terminal drop_point_id=%s submission_id=%s reason=%s", id, submissionID, reason)
	}
}

func pickupFinalizationContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), pickupFinalizationTimeout)
}

func writePickupMultipart(w http.ResponseWriter, envelope []byte, payload io.Reader) error {
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if err := writePickupPart(writer, envelopePartName, jsonContentType, envelope); err != nil {
		return err
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", octetContentType)
	header.Set("Content-Disposition", fmt.Sprintf(`attachment; name="%s"`, payloadPartName))
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, payload); err != nil {
		return err
	}
	return writer.Close()
}

func writePickupPart(writer *multipart.Writer, name string, contentType string, data []byte) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", fmt.Sprintf(`attachment; name="%s"`, name))
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func submissionStatusError(status droppoint.SubmissionStatus) error {
	switch status {
	case droppoint.SubmissionStatusAcknowledged:
		return droppoint.ErrSubmissionAcknowledged
	case droppoint.SubmissionStatusFailed:
		return droppoint.ErrSubmissionFailed
	default:
		return droppoint.ErrSubmissionNotReady
	}
}
