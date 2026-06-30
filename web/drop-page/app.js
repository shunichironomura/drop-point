const PROTOCOL_VERSION = 2;
const KEY_AGREEMENT = 'x25519-hkdf-sha256-aesgcm-raw32';
const INFO_METADATA = new TextEncoder().encode('DropPoint/protocol/v2 key=metadata');
const INFO_PAYLOAD = new TextEncoder().encode('DropPoint/protocol/v2 key=payload');
const AAD_METADATA = aad('metadata');
const AAD_PAYLOAD = aad('payload');

const state = {
  recipientPublicKey: null,
  dropToken: location.pathname.split('/').pop(),
};

const filesInput = document.getElementById('files');
const submitButton = document.getElementById('submit');
const statusBox = document.getElementById('status');

init().catch((error) => showError(error.message || 'This drop point cannot be used.'));

submitButton.addEventListener('click', () => {
  dropSelectedFiles().catch((error) => showError(error.message || 'Dropping files failed.'));
});

async function init() {
  if (!window.isSecureContext || !crypto?.subtle) {
    throw new Error('This page must be opened over HTTPS or localhost to encrypt files.');
  }
  state.recipientPublicKey = parseFragmentPublicKey(location.hash);
  await assertX25519Support(state.recipientPublicKey);
  filesInput.disabled = false;
  submitButton.disabled = false;
  showStatus('Choose files');
}

async function dropSelectedFiles() {
  const files = [...filesInput.files];
  if (files.length === 0) {
    throw new Error('Choose files before dropping.');
  }
  filesInput.disabled = true;
  submitButton.disabled = true;
  showStatus('Encrypting and dropping files...');
  const bundle = await buildEncryptedBundle(files, state.recipientPublicKey);
  showStatus('Dropping encrypted files...');
  const form = new FormData();
  form.append('envelope', new Blob([JSON.stringify(bundle.envelope)], { type: 'application/json' }));
  form.append('payload', new Blob([bundle.encryptedPayload], { type: 'application/octet-stream' }));
  const response = await fetch(`/api/drops/${encodeURIComponent(state.dropToken)}`, {
    method: 'PUT',
    body: form,
    credentials: 'omit',
    cache: 'no-store',
  });
  if (!response.ok) {
    if (response.status === 404 || response.status === 410) {
      throw new Error('This drop point has expired.');
    }
    throw new Error('Network failure or drop point rejected the encrypted files.');
  }
  showSuccess('Files dropped successfully. Ready for pickup');
}

function parseFragmentPublicKey(hash) {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash);
  if (params.get('v') !== String(PROTOCOL_VERSION)) {
    throw new Error('This drop link is missing its public key.');
  }
  const publicKey = decodeBase64URL(params.get('pk') || '');
  if (publicKey.byteLength !== 32) {
    throw new Error('This drop link is missing its public key.');
  }
  return publicKey;
}

async function assertX25519Support(recipientPublicKey) {
  try {
    await crypto.subtle.importKey('raw', recipientPublicKey, { name: 'X25519' }, false, []);
  } catch (_error) {
    throw new Error('This browser does not support DropPoint encryption. Use a current browser with WebCrypto X25519 support.');
  }
}

async function buildEncryptedBundle(files, recipientPublicKeyBytes) {
  const manifestFiles = [];
  const chunks = [];
  for (const file of files) {
    const bytes = new Uint8Array(await file.arrayBuffer());
    manifestFiles.push({ name: file.name || 'file', type: file.type || 'application/octet-stream', size: bytes.byteLength });
    chunks.push(bytes);
  }
  const payloadPlaintext = concat(chunks);
  const manifest = {
    protocol_version: PROTOCOL_VERSION,
    files: manifestFiles,
    created_at: new Date().toISOString(),
  };
  const manifestBytes = new TextEncoder().encode(JSON.stringify(manifest));

  const recipientPublicKey = await crypto.subtle.importKey('raw', recipientPublicKeyBytes, { name: 'X25519' }, false, []);
  const senderKeyPair = await crypto.subtle.generateKey({ name: 'X25519' }, true, ['deriveBits']);
  const senderPublicKey = new Uint8Array(await crypto.subtle.exportKey('raw', senderKeyPair.publicKey));
  const sharedSecret = new Uint8Array(await crypto.subtle.deriveBits({ name: 'X25519', public: recipientPublicKey }, senderKeyPair.privateKey, 256));
  if (sharedSecret.every((byte) => byte === 0)) {
    throw new Error('This drop link uses an invalid public key.');
  }
  const salt = concat([senderPublicKey, recipientPublicKeyBytes]);
  const hkdfKey = await crypto.subtle.importKey('raw', sharedSecret, 'HKDF', false, ['deriveKey']);
  const metadataKey = await deriveAESKey(hkdfKey, salt, INFO_METADATA);
  const payloadKey = await deriveAESKey(hkdfKey, salt, INFO_PAYLOAD);
  const metadataNonce = crypto.getRandomValues(new Uint8Array(12));
  const payloadNonce = crypto.getRandomValues(new Uint8Array(12));
  const encryptedMetadata = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv: metadataNonce, additionalData: AAD_METADATA, tagLength: 128 }, metadataKey, manifestBytes));
  const encryptedPayload = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv: payloadNonce, additionalData: AAD_PAYLOAD, tagLength: 128 }, payloadKey, payloadPlaintext));
  return {
    envelope: {
      protocol_version: PROTOCOL_VERSION,
      key_agreement: KEY_AGREEMENT,
      sender_ephemeral_public_key: encodeBase64URL(senderPublicKey),
      metadata_nonce: encodeBase64URL(metadataNonce),
      payload_nonce: encodeBase64URL(payloadNonce),
      encrypted_metadata: encodeBase64URL(encryptedMetadata),
    },
    encryptedPayload,
  };
}

async function deriveAESKey(hkdfKey, salt, info) {
  return crypto.subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt, info },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt'],
  );
}

function aad(label) {
  const text = new TextEncoder().encode(label);
  const out = new Uint8Array(1 + text.byteLength);
  out[0] = PROTOCOL_VERSION;
  out.set(text, 1);
  return out;
}

function concat(chunks) {
  const length = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

function decodeBase64URL(value) {
  if (!value || value.includes('=')) {
    return new Uint8Array();
  }
  const padded = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(value.length / 4) * 4, '=');
  const binary = atob(padded);
  return Uint8Array.from(binary, (char) => char.charCodeAt(0));
}

function encodeBase64URL(bytes) {
  let binary = '';
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

function showStatus(message) {
  statusBox.className = 'status';
  statusBox.textContent = message;
}

function showSuccess(message) {
  statusBox.className = 'status success';
  statusBox.textContent = message;
}

function showError(message) {
  filesInput.disabled = true;
  submitButton.disabled = true;
  statusBox.className = 'status error';
  statusBox.textContent = message;
}
