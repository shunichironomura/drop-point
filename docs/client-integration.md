# Generic client integration boundary

DropPoint is a generic temporary ciphertext relay. Client applications own local key management, plaintext storage, attachment/event models, and user workflows.

## Receiver flow

A receiver client should run this sequence for each drop point:

1. Generate a fresh local X25519 recipient key pair for this drop point.
2. Call `POST /api/drop-points` with an enabled API bearer token.
3. Keep the returned `pickup_token`, `display_name`, and local recipient private key in receiver-controlled state.
4. Show the returned `display_name` to the receiver and tell the sender to compare it with the name shown on the drop page.
5. Append the public-key and expiry fragment to the returned fragment-free drop link:

   ```text
   #v=2&pk=<base64url(raw-32-byte-x25519-public-key)>&exp=<urlencoded expires_at>
   ```

   `exp` is optional for compatibility; current sender pages fetch the authoritative expiry and display name from the relay.

6. Show or share the full drop link, for example as a QR code.
7. Poll `GET /api/drop-points/:drop_point_id/status` with the pickup token.
8. When `pending_submissions` is nonzero, call `GET /api/drop-points/:drop_point_id/submissions` and process each returned child independently.
9. Call `GET /api/drop-points/:drop_point_id/submissions/:submission_id/pickup` for one ready child.
10. Parse the `multipart/mixed` response into envelope JSON and encrypted payload bytes.
11. Decrypt locally with the recipient private key using the protocol in `docs/protocol-reference.md`.
12. Validate the decrypted manifest:
    - `protocol_version` is `2`;
    - filenames are safe base names;
    - duplicate filenames are rejected or disambiguated;
    - MIME types are advisory and sanitized;
    - sum of manifest file sizes equals decrypted payload length.
13. Split plaintext bytes by manifest sizes.
14. Stage the complete bundle in an owner-only, receiver-controlled directory; write and fsync every file, write a durable identity/receipt keyed by `submission_id`, and fsync the staging directory.
15. Atomically publish the complete bundle directory without merging into or overwriting a different existing bundle, then fsync its parent directory.
16. Atomically and durably record the installed submission identity in private receiver state so a retry can verify an already-installed identical bundle.
17. Append any client-specific durable record only after plaintext storage succeeds.
18. Call `DELETE /api/drop-points/:drop_point_id/submissions/:submission_id` to acknowledge the child and free its queue capacity.
19. Repeat from listing while the reusable session remains active.
20. When no more submissions should be accepted, call `DELETE /api/drop-points/:drop_point_id` to close the session and remove any remaining remote ciphertext.
21. Delete the local recipient private key and any temporary plaintext buffers only after close succeeds or expiry is confirmed.

## Ordering rule

Do not acknowledge a submission before the client has durably stored its complete decrypted bundle and any local record needed to find it. Do not merge files one at a time into a pre-existing destination or blindly overwrite an existing bundle. A durable identity receipt keyed by `submission_id` must let a retry recognize the same already-installed bundle and resume acknowledgement without rewriting plaintext. Pickup is repeatable until acknowledgement, close, or expiry. Acknowledging one child leaves the parent open for more submissions; close only when the reusable session is finished.

## Client model boundary

DropPoint does not define client-specific event schemas, attachment records, note models, account models, or durable plaintext storage. Those models belong to the integrating client.

For Procnote-like attachment clients, append the durable local attachment event only after:

1. pickup succeeds;
2. decryption and AES-GCM authentication succeed;
3. manifest validation succeeds;
4. filename and MIME sanitization succeed;
5. plaintext attachment bytes are durably stored in the client system.

Only after those steps should the client acknowledge that remote submission. This preserves recovery if local storage fails after pickup while keeping the drop point available for later submissions.
