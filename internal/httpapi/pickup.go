package httpapi

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/shunichironomura/drop-point/internal/droppoint"
)

// HandlePickupPayload handles GET /api/drop-points/:drop_point_id/pickup.
func HandlePickupPayload(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := dropPointIDFromPickupPath(r.URL.Path)
		dp, ok := authorizePickup(w, r, deps, id)
		if !ok {
			return
		}
		if dp.Status != droppoint.StatusReady {
			writePickupUnavailable(w, dp.Status)
			return
		}
		if deps.BlobStore == nil || dp.EnvelopePath == "" || dp.PayloadPath == "" {
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		envelope, err := deps.BlobStore.ReadEnvelope(r.Context(), dp.EnvelopePath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored envelope is unavailable")
			return
		}
		payload, err := deps.BlobStore.OpenPayload(r.Context(), dp.PayloadPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "payload_unavailable", "stored payload is unavailable")
			return
		}
		defer payload.Close()

		if err := writePickupMultipart(w, envelope, payload); err != nil {
			return
		}
		if err := deps.Repository.MarkFirstPickedUp(r.Context(), id, deps.Now().UTC()); err != nil && deps.Logger != nil {
			deps.Logger.Printf("pickup timestamp update failed drop_point_id=%s error=%s", id, err)
		}
	}
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
	default:
		writeError(w, http.StatusConflict, "drop_not_ready", "drop point is not ready for pickup")
	}
}

func dropPointIDFromPickupPath(path string) string {
	if !strings.HasPrefix(path, "/api/drop-points/") || !strings.HasSuffix(path, "/pickup") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/drop-points/"), "/pickup")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}
