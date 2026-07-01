# Generic client integration boundary

DropPoint is a generic temporary ciphertext relay. Client applications own local key management, plaintext storage, attachment/event models, and user workflows.

## Receiver flow

A receiver client should run this sequence for each drop point:

1. Generate a fresh local X25519 recipient key pair for this drop point.
2. Call `POST /api/drop-points` with the configured API bearer token.
3. Keep the returned `pickup_token` and local recipient private key in receiver-controlled state.
4. Append the public-key and expiry fragment to the returned fragment-free drop link:

   ```text
   #v=2&pk=<base64url(raw-32-byte-x25519-public-key)>&exp=<urlencoded expires_at>
   ```

   `exp` is optional for older clients, but including the returned `expires_at` lets the sender page display an expiry countdown.

5. Show or share the full drop link, for example as a QR code.
6. Poll `GET /api/drop-points/:drop_point_id/status` with the pickup token.
7. When status is `ready`, call `GET /api/drop-points/:drop_point_id/pickup`.
8. Parse the `multipart/mixed` response into envelope JSON and encrypted payload bytes.
9. Decrypt locally with the recipient private key using the protocol in `docs/protocol-reference.md`.
10. Validate the decrypted manifest:
    - `protocol_version` is `2`;
    - filenames are safe base names;
    - duplicate filenames are rejected or disambiguated;
    - MIME types are advisory and sanitized;
    - sum of manifest file sizes equals decrypted payload length.
11. Split plaintext bytes by manifest sizes.
12. Write plaintext durably into the client-controlled storage system.
13. Append any client-specific durable record only after plaintext storage succeeds.
14. Call `DELETE /api/drop-points/:drop_point_id` to close and remove remote ciphertext.
15. Delete the local recipient private key and any temporary plaintext buffers.

## Ordering rule

Do not close the remote drop point before the client has durably stored the decrypted files and any local record needed to find them. Pickup is repeatable until close or expiry, so clients can retry local processing without asking the sender to upload again.

## Client model boundary

DropPoint does not define client-specific event schemas, attachment records, note models, account models, or durable plaintext storage. Those models belong to the integrating client.

For Procnote-like attachment clients, append the durable local attachment event only after:

1. pickup succeeds;
2. decryption and AES-GCM authentication succeed;
3. manifest validation succeeds;
4. filename and MIME sanitization succeed;
5. plaintext attachment bytes are durably stored in the client system.

Only after those steps should the client close the remote drop point. This preserves recovery if local storage fails after pickup.
