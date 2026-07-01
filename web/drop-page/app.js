const PROTOCOL_VERSION = 2;
const KEY_AGREEMENT = 'x25519-hkdf-sha256-aesgcm-raw32';
const COUNTDOWN_INTERVAL_MS = 1000;
const INFO_METADATA = new TextEncoder().encode('DropPoint/protocol/v2 key=metadata');
const INFO_PAYLOAD = new TextEncoder().encode('DropPoint/protocol/v2 key=payload');
const AAD_METADATA = aad('metadata');
const AAD_PAYLOAD = aad('payload');

const state = {
  recipientPublicKey: null,
  expiresAt: null,
  countdownTimer: null,
  selectedFiles: [],
  dropToken: location.pathname.split('/').pop(),
};

const filesInput = document.getElementById('files');
const submitButton = document.getElementById('submit');
const statusBox = document.getElementById('status');
const expiryBox = document.getElementById('expiry');
const countdownText = document.getElementById('countdown');
const dropZone = document.getElementById('drop-zone');
const selectionBox = document.getElementById('selection');
const selectedFilesList = document.getElementById('selected-files');

init().catch((error) => showError(error.message || 'This drop point cannot be used.'));

filesInput.addEventListener('change', () => setSelectedFiles([...state.selectedFiles, ...filesInput.files]));
dropZone.addEventListener('dragenter', handleDragOverFiles);
dropZone.addEventListener('dragover', handleDragOverFiles);
dropZone.addEventListener('dragleave', () => dropZone.classList.remove('drag-over'));
dropZone.addEventListener('drop', handleDroppedFiles);
window.addEventListener('dragover', preventFileNavigation);
window.addEventListener('drop', preventFileNavigation);

submitButton.addEventListener('click', () => {
  dropSelectedFiles().catch((error) => showError(error.message || 'Dropping files failed.'));
});

async function init() {
  if (!window.isSecureContext || !crypto?.subtle) {
    throw new Error('This page must be opened over HTTPS or localhost to encrypt files.');
  }
  const fragment = parseDropLinkFragment(location.hash);
  state.recipientPublicKey = fragment.recipientPublicKey;
  state.expiresAt = fragment.expiresAt;
  if (isExpired(state.expiresAt)) {
    throw new Error('This drop point has expired.');
  }
  await assertX25519Support(state.recipientPublicKey);
  startExpiryCountdown(state.expiresAt);
  filesInput.disabled = false;
  dropZone.classList.remove('disabled');
  updateSelectedFiles();
  showStatus('Choose files');
}

function handleDragOverFiles(event) {
  if (filesInput.disabled) {
    return;
  }
  event.preventDefault();
  event.dataTransfer.dropEffect = 'copy';
  dropZone.classList.add('drag-over');
}

function handleDroppedFiles(event) {
  if (filesInput.disabled) {
    return;
  }
  event.preventDefault();
  dropZone.classList.remove('drag-over');
  const droppedFiles = Array.from(event.dataTransfer.files);
  if (droppedFiles.length === 0) {
    return;
  }
  setSelectedFiles([...state.selectedFiles, ...droppedFiles]);
}

function setSelectedFiles(files) {
  state.selectedFiles = files;
  filesInput.value = '';
  updateSelectedFiles();
}

function preventFileNavigation(event) {
  if (Array.from(event.dataTransfer?.types || []).includes('Files')) {
    event.preventDefault();
  }
}

function updateSelectedFiles() {
  const files = [...state.selectedFiles];
  submitButton.disabled = files.length === 0;
  renderSelectedFiles(files);
  if (files.length === 0) {
    showStatus('Choose files');
    return;
  }
  showStatus(`${files.length} ${files.length === 1 ? 'file' : 'files'} selected. Ready to drop encrypted files.`);
}

function renderSelectedFiles(files) {
  selectedFilesList.replaceChildren(...files.map((file, index) => {
    const item = document.createElement('li');
    item.className = 'selected-file';

    const details = document.createElement('span');
    details.className = 'file-details';
    const name = document.createElement('span');
    name.textContent = file.name || 'file';
    const size = document.createElement('span');
    size.className = 'file-size';
    size.textContent = ` (${formatBytes(file.size)})`;
    details.append(name, size);

    const removeButton = document.createElement('button');
    removeButton.type = 'button';
    removeButton.className = 'remove-file';
    removeButton.textContent = 'Remove';
    removeButton.disabled = filesInput.disabled;
    removeButton.setAttribute('aria-label', `Remove ${file.name || 'file'} from selected files`);
    removeButton.addEventListener('click', () => removeSelectedFile(index));

    item.append(details, removeButton);
    return item;
  }));
  selectionBox.hidden = files.length === 0;
}

function removeSelectedFile(index) {
  if (filesInput.disabled) {
    return;
  }
  state.selectedFiles = state.selectedFiles.filter((_file, fileIndex) => fileIndex !== index);
  filesInput.value = '';
  updateSelectedFiles();
}

async function dropSelectedFiles() {
  const files = [...state.selectedFiles];
  if (files.length === 0) {
    throw new Error('Choose files before dropping.');
  }
  filesInput.disabled = true;
  submitButton.disabled = true;
  dropZone.classList.add('disabled');
  renderSelectedFiles(files);
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

function parseDropLinkFragment(hash) {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash);
  if (params.get('v') !== String(PROTOCOL_VERSION)) {
    throw new Error('This drop link is missing its public key.');
  }
  const recipientPublicKey = decodeBase64URL(params.get('pk') || '');
  if (recipientPublicKey.byteLength !== 32) {
    throw new Error('This drop link is missing its public key.');
  }
  return { recipientPublicKey, expiresAt: parseExpiry(params.get('exp')) };
}

function parseExpiry(value) {
  if (!value) {
    return null;
  }
  const millis = Date.parse(value);
  if (!Number.isFinite(millis)) {
    throw new Error('This drop link has an invalid expiry timestamp.');
  }
  return new Date(millis);
}

function isExpired(expiresAt) {
  return expiresAt !== null && expiresAt.getTime() <= Date.now();
}

function startExpiryCountdown(expiresAt) {
  if (!expiresAt) {
    expiryBox.hidden = true;
    return;
  }
  expiryBox.hidden = false;
  renderExpiryCountdown();
  state.countdownTimer = window.setInterval(renderExpiryCountdown, COUNTDOWN_INTERVAL_MS);
}

function renderExpiryCountdown() {
  const remainingMs = state.expiresAt.getTime() - Date.now();
  if (remainingMs <= 0) {
    stopExpiryCountdown();
    countdownText.textContent = 'expired';
    showError('This drop point has expired.');
    return;
  }
  countdownText.textContent = formatRemainingTime(remainingMs);
}

function stopExpiryCountdown() {
  if (state.countdownTimer !== null) {
    window.clearInterval(state.countdownTimer);
    state.countdownTimer = null;
  }
}

function formatRemainingTime(remainingMs) {
  const totalSeconds = Math.max(0, Math.ceil(remainingMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) {
    return `${hours}h ${minutes}m ${seconds}s`;
  }
  if (minutes > 0) {
    return `${minutes}m ${seconds}s`;
  }
  return `${seconds}s`;
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

function formatBytes(size) {
  if (!Number.isFinite(size) || size < 0) {
    return 'unknown size';
  }
  if (size < 1024) {
    return `${size} B`;
  }
  const units = ['KiB', 'MiB', 'GiB'];
  let value = size / 1024;
  for (const unit of units) {
    if (value < 1024 || unit === units[units.length - 1]) {
      return `${value.toFixed(value < 10 ? 1 : 0)} ${unit}`;
    }
    value /= 1024;
  }
  return `${size} B`;
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
  stopExpiryCountdown();
  filesInput.disabled = true;
  submitButton.disabled = true;
  dropZone.classList.add('disabled');
  renderSelectedFiles([...state.selectedFiles]);
  statusBox.className = 'status error';
  statusBox.textContent = message;
}
