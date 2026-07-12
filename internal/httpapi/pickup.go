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

	"github.com/shunichironomura/droppoint/internal/droppoint"
)

const pickupFinalizationTimeout = 10 * time.Second

// HandlePickupPayload handles GET /api/drop-points/:drop_point_id/pickup.
func HandlePickupPayload(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("drop_point_id")
		dp, ok := authorizePickup(w, r, deps, id)
		if !ok {
			return
		}
		if dp.Status != droppoint.StatusReady {
			writePickupUnavailable(w, dp.Status)
			return
		}
		if dp.EnvelopePath == "" || dp.PayloadPath == "" {
			failCorruptDropPoint(r.Context(), deps, id, "missing_blob_pointer")
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		if deps.BlobStore == nil {
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
		envelope, err := deps.BlobStore.ReadEnvelope(r.Context(), dp.EnvelopePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				failCorruptDropPoint(r.Context(), deps, id, "missing_envelope_blob")
			}
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored envelope is unavailable")
			return
		}
		payload, err := deps.BlobStore.OpenPayload(r.Context(), dp.PayloadPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				failCorruptDropPoint(r.Context(), deps, id, "missing_payload_blob")
			}
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		defer payload.Close()

		if err := writePickupMultipart(w, envelope, payload); err != nil {
			if deps.Logger != nil {
				deps.Logger.Printf("event=pickup.response_failed drop_point_id=%s error=%q", id, err)
			}
			return
		}
		finalizeCtx, cancel := pickupFinalizationContext(r.Context())
		defer cancel()
		if err := deps.Repository.MarkFirstPickedUp(finalizeCtx, id, deps.Now().UTC()); err != nil && deps.Logger != nil {
			deps.Logger.Printf("event=pickup.timestamp_failed drop_point_id=%s error=%q", id, err)
		}
	}
}

func failCorruptDropPoint(parent context.Context, deps Dependencies, id string, reason string) {
	ctx, cancel := pickupFinalizationContext(parent)
	defer cancel()
	if err := deps.Repository.FailDropPoint(ctx, id, deps.Now().UTC()); err != nil {
		if deps.Logger != nil {
			deps.Logger.Printf("event=drop.fail_transition_failed drop_point_id=%s reason=%s error=%q", id, reason, err)
		}
		return
	}
	if deps.Logger != nil {
		deps.Logger.Printf("event=drop.failed_terminal drop_point_id=%s reason=%s", id, reason)
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

func writePickupUnavailable(w http.ResponseWriter, status droppoint.Status) {
	switch status {
	case droppoint.StatusOpen, droppoint.StatusReceiving:
		writeError(w, http.StatusConflict, "drop_not_ready", "drop point is not ready for pickup")
	case droppoint.StatusClosed:
		writeError(w, http.StatusGone, "drop_point_closed", "drop point is closed")
	case droppoint.StatusExpired:
		writeError(w, http.StatusGone, "drop_point_expired", "drop point has expired")
	case droppoint.StatusFailed:
		writeError(w, http.StatusGone, "drop_point_failed", "drop point failed internally")
	default:
		writeError(w, http.StatusConflict, "drop_not_ready", "drop point is not ready for pickup")
	}
}
