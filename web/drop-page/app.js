const PROTOCOL_VERSION = 2;
const KEY_AGREEMENT = 'x25519-hkdf-sha256-aesgcm-raw32';
const COUNTDOWN_INTERVAL_MS = 1000;
const IMAGE_TYPE_PREFIX = 'image/';
const IMAGE_NAME_PATTERN = /\.(?:apng|avif|bmp|gif|heic|heif|ico|jpe?g|png|svg|webp)$/i;
const BASE64URL_PATTERN = /^[A-Za-z0-9_-]+$/;
const MAX_MANIFEST_FILES = 1000;
const MAX_FILENAME_UTF8_BYTES = 240;
const MAX_FILENAME_EXTENSION_BYTES = 32;
const MAX_MIME_TYPE_UTF8_BYTES = 255;
const SAFE_MIME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*\/[A-Za-z0-9][A-Za-z0-9!#$&^_.+-]*$/;
const RESERVED_RECEIPT_NAME = '.droppoint-receipt.json';
const WINDOWS_RESERVED_NAMES = new Set([
  'CON', 'PRN', 'AUX', 'NUL',
  'COM1', 'COM2', 'COM3', 'COM4', 'COM5', 'COM6', 'COM7', 'COM8', 'COM9', 'COM¹', 'COM²', 'COM³',
  'LPT1', 'LPT2', 'LPT3', 'LPT4', 'LPT5', 'LPT6', 'LPT7', 'LPT8', 'LPT9', 'LPT¹', 'LPT²', 'LPT³',
]);
const INFO_METADATA = new TextEncoder().encode('DropPoint/protocol/v2 key=metadata');
const INFO_PAYLOAD = new TextEncoder().encode('DropPoint/protocol/v2 key=payload');
const AAD_METADATA = aad('metadata');
const AAD_PAYLOAD = aad('payload');

const state = {
  recipientPublicKey: null,
  displayName: null,
  expiresAt: null,
  maxBytes: null,
  countdownTimer: null,
  selectedFiles: [],
  thumbnailURLs: new Map(),
  dropToken: location.pathname.split('/').pop(),
};

class DropPointUserError extends Error {}

function userError(message) {
  return new DropPointUserError(message);
}

function userErrorMessage(error, fallback) {
  if (error instanceof DropPointUserError) {
    return error.message;
  }
  return fallback;
}

const filesInput = document.getElementById('files');
const submitButton = document.getElementById('submit');
const statusBox = document.getElementById('status');
const dropNameBox = document.getElementById('drop-name');
const dropNameText = document.getElementById('drop-name-text');
const expiryBox = document.getElementById('expiry');
const countdownText = document.getElementById('countdown');
const dropZone = document.getElementById('drop-zone');
const selectionBox = document.getElementById('selection');
const selectedFilesList = document.getElementById('selected-files');

init().catch((error) => showError(userErrorMessage(error, 'This drop point cannot be used.')));

filesInput.addEventListener('change', () => setSelectedFiles([...state.selectedFiles, ...filesInput.files]));
dropZone.addEventListener('dragenter', handleDragOverFiles);
dropZone.addEventListener('dragover', handleDragOverFiles);
dropZone.addEventListener('dragleave', () => dropZone.classList.remove('drag-over'));
dropZone.addEventListener('drop', handleDroppedFiles);
window.addEventListener('dragover', preventFileNavigation);
window.addEventListener('drop', preventFileNavigation);
window.addEventListener('pagehide', revokeAllThumbnailURLs);

submitButton.addEventListener('click', () => {
  dropSelectedFiles().catch((error) => showError(userErrorMessage(error, 'Dropping files failed.')));
});

async function init() {
  if (!window.isSecureContext || !crypto?.subtle) {
    throw userError('This page must be opened over HTTPS or localhost to encrypt files.');
  }
  const fragment = parseDropLinkFragment(location.hash);
  state.recipientPublicKey = fragment.recipientPublicKey;
  const metadata = await fetchDropMetadata(state.dropToken);
  state.displayName = metadata.displayName;
  state.expiresAt = metadata.expiresAt ?? fragment.expiresAt;
  state.maxBytes = metadata.maxBytes;
  if (isExpired(state.expiresAt)) {
    throw userError('This drop point has expired.');
  }
  await assertX25519Support(state.recipientPublicKey);
  renderDropName();
  startExpiryCountdown(state.expiresAt);
  filesInput.disabled = false;
  dropZone.classList.remove('disabled');
  updateSelectedFiles();
  showStatus(`Choose files for ${state.displayName}`);
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
  const limitMessage = selectedFilesLimitMessage(files);
  submitButton.disabled = files.length === 0 || limitMessage !== null;
  renderSelectedFiles(files);
  if (files.length === 0) {
    showStatus(state.displayName ? `Choose files for ${state.displayName}` : 'Choose files');
    return;
  }
  if (limitMessage !== null) {
    showSelectionError(limitMessage);
    return;
  }
  showStatus(`${files.length} ${files.length === 1 ? 'file' : 'files'} selected for ${state.displayName}. Ready to drop encrypted files.`);
}

function renderSelectedFiles(files) {
  pruneThumbnailURLs(files);
  selectedFilesList.replaceChildren(...files.map((file, index) => {
    const item = document.createElement('li');
    item.className = 'selected-file';

    const summary = document.createElement('span');
    summary.className = 'file-summary';
    const thumbnail = createFileThumbnail(file);
    if (thumbnail !== null) {
      summary.append(thumbnail);
    }

    const details = document.createElement('span');
    details.className = 'file-details';
    const name = document.createElement('span');
    name.textContent = file.name || 'file';
    const size = document.createElement('span');
    size.className = 'file-size';
    size.textContent = ` (${formatBytes(file.size)})`;
    details.append(name, size);
    summary.append(details);

    const removeButton = document.createElement('button');
    removeButton.type = 'button';
    removeButton.className = 'remove-file';
    removeButton.textContent = 'Remove';
    removeButton.disabled = filesInput.disabled;
    removeButton.setAttribute('aria-label', `Remove ${file.name || 'file'} from selected files`);
    removeButton.addEventListener('click', () => removeSelectedFile(index));

    item.append(summary, removeButton);
    return item;
  }));
  selectionBox.hidden = files.length === 0;
}

function createFileThumbnail(file) {
  if (!isImageFile(file) || typeof URL.createObjectURL !== 'function') {
    return null;
  }
  const image = document.createElement('img');
  image.className = 'file-thumbnail';
  image.alt = '';
  image.decoding = 'async';
  image.src = thumbnailURLFor(file);
  image.addEventListener('error', () => {
    image.hidden = true;
  });
  return image;
}

function thumbnailURLFor(file) {
  const existingURL = state.thumbnailURLs.get(file);
  if (existingURL) {
    return existingURL;
  }
  const url = URL.createObjectURL(file);
  state.thumbnailURLs.set(file, url);
  return url;
}

function isImageFile(file) {
  const mimeType = file.type || '';
  return mimeType.startsWith(IMAGE_TYPE_PREFIX) || IMAGE_NAME_PATTERN.test(file.name || '');
}

function pruneThumbnailURLs(files) {
  const currentFiles = new Set(files.filter(isImageFile));
  for (const [file, url] of state.thumbnailURLs) {
    if (!currentFiles.has(file)) {
      URL.revokeObjectURL(url);
      state.thumbnailURLs.delete(file);
    }
  }
}

function revokeAllThumbnailURLs() {
  for (const url of state.thumbnailURLs.values()) {
    URL.revokeObjectURL(url);
  }
  state.thumbnailURLs.clear();
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
    throw userError('Choose files before dropping.');
  }
  const limitMessage = selectedFilesLimitMessage(files);
  if (limitMessage !== null) {
    showSelectionError(limitMessage);
    return;
  }
  filesInput.disabled = true;
  submitButton.disabled = true;
  dropZone.classList.add('disabled');
  renderSelectedFiles(files);
  showStatus(`Encrypting and dropping files for ${state.displayName}...`);
  const bundle = await buildEncryptedBundle(files, state.recipientPublicKey);
  showStatus(`Dropping encrypted files for ${state.displayName}...`);
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
      throw userError('This drop point has expired.');
    }
    if (response.status === 409) {
      throw userError('This drop point cannot accept more files.');
    }
    if (response.status === 413) {
      throw userError(`Encrypted files exceeded the ${formatBytes(state.maxBytes)} drop point limit.`);
    }
    throw userError('Network failure or drop point rejected the encrypted files.');
  }
  stopExpiryCountdown();
  showSuccess(`Files dropped successfully for ${state.displayName}. Ready for pickup`);
}

function selectedFilesLimitMessage(files) {
  if (!Number.isFinite(state.maxBytes) || state.maxBytes <= 0 || files.length === 0) {
    return null;
  }
  const estimatedEncryptedBytes = estimatedEncryptedPayloadBytes(files);
  if (estimatedEncryptedBytes <= state.maxBytes) {
    return null;
  }
  return `Selected files are too large for this drop point (${formatBytes(estimatedEncryptedBytes)} encrypted, limit ${formatBytes(state.maxBytes)}).`;
}

function estimatedEncryptedPayloadBytes(files) {
  const plaintextBytes = files.reduce((sum, file) => sum + Math.max(0, Number(file.size) || 0), 0);
  return plaintextBytes + 16;
}

async function fetchDropMetadata(dropToken) {
  const response = await fetch(`/api/drops/${encodeURIComponent(dropToken)}`, {
    method: 'GET',
    headers: { Accept: 'application/json' },
    credentials: 'omit',
    cache: 'no-store',
  });
  if (!response.ok) {
    if (response.status === 404 || response.status === 410) {
      throw userError('This drop point has expired or cannot be used.');
    }
    if (response.status === 409) {
      throw userError('This drop point cannot accept more files.');
    }
    throw userError('Could not load this drop point.');
  }
  let metadata;
  try {
    metadata = await response.json();
  } catch (_error) {
    throw userError('Could not load this drop point.');
  }
  const expiresAt = parseExpiry(metadata.expires_at);
  if (expiresAt === null) {
    throw userError('This drop point returned an invalid expiry timestamp.');
  }
  return {
    displayName: parseDisplayName(metadata.display_name),
    expiresAt,
    maxBytes: parseMaxBytes(metadata.max_bytes),
  };
}

function parseDisplayName(value) {
  if (typeof value !== 'string' || !/^[a-z]+-[a-z]+$/.test(value)) {
    throw userError('This drop point returned an invalid name.');
  }
  return value;
}

function parseMaxBytes(value) {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value <= 0) {
    throw userError('This drop point returned an invalid size limit.');
  }
  return value;
}

function renderDropName() {
  dropNameText.textContent = state.displayName;
  dropNameBox.hidden = false;
}

function parseDropLinkFragment(hash) {
  const params = new URLSearchParams(hash.startsWith('#') ? hash.slice(1) : hash);
  if (params.get('v') !== String(PROTOCOL_VERSION)) {
    throw userError('This drop link is missing its public key.');
  }
  const recipientPublicKey = decodeBase64URL(params.get('pk') || '');
  if (recipientPublicKey.byteLength !== 32) {
    throw userError('This drop link is missing its public key.');
  }
  return { recipientPublicKey, expiresAt: parseExpiry(params.get('exp')) };
}

function parseExpiry(value) {
  if (!value) {
    return null;
  }
  const millis = Date.parse(value);
  if (!Number.isFinite(millis)) {
    throw userError('This drop point has an invalid expiry timestamp.');
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
    throw userError('This browser does not support DropPoint encryption. Use a current browser with WebCrypto X25519 support.');
  }
}

async function buildEncryptedBundle(files, recipientPublicKeyBytes) {
  if (files.length === 0 || files.length > MAX_MANIFEST_FILES) {
    throw userError(`Choose between 1 and ${MAX_MANIFEST_FILES} files.`);
  }
  const manifestFiles = [];
  const chunks = [];
  const usedManifestNames = new Set();
  for (const file of files) {
    const bytes = new Uint8Array(await file.arrayBuffer());
    manifestFiles.push({ name: uniqueManifestName(file.name || 'file', usedManifestNames), type: canonicalManifestMIME(file.type), size: bytes.byteLength });
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
    throw userError('This drop link uses an invalid public key.');
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

function canonicalManifestMIME(value) {
  const mediaType = String(value || 'application/octet-stream').toLowerCase();
  if (utf8Length(mediaType) > MAX_MIME_TYPE_UTF8_BYTES || !SAFE_MIME_PATTERN.test(mediaType)) {
    return 'application/octet-stream';
  }
  return mediaType;
}

function uniqueManifestName(name, usedNames) {
  const baseName = sanitizeManifestName(name);
  let candidate = baseName;
  let suffix = 2;
  while (usedNames.has(foldManifestName(candidate))) {
    candidate = appendNameSuffix(baseName, suffix);
    suffix += 1;
  }
  usedNames.add(foldManifestName(candidate));
  return candidate;
}

function sanitizeManifestName(value) {
  const normalized = String(value || 'file').normalize('NFC');
  let name = normalized
    .replace(/[\/\\\x00<>:"|?*]/gu, '_')
    .replace(/[\p{Cc}\p{Cf}\p{Cs}]/gu, '_')
    .replace(/[ .]+$/u, '');
  if (/^\p{White_Space}*$/u.test(name) || name === '.' || name === '..') {
    name = 'file';
  }
  if (isReservedWindowsName(name) || name.toLowerCase() === RESERVED_RECEIPT_NAME) {
    name = `_${name}`;
  }
  return fitManifestName(name, '');
}

function isReservedWindowsName(name) {
  const base = name.split('.', 1)[0].replace(/ +$/u, '').toUpperCase();
  return WINDOWS_RESERVED_NAMES.has(base);
}

function appendNameSuffix(name, suffix) {
  return fitManifestName(name, ` (${suffix})`);
}

function fitManifestName(name, suffix) {
  let stem = name;
  let extension = '';
  const dot = name.lastIndexOf('.');
  if (dot > 0) {
    const possibleExtension = name.slice(dot);
    if (utf8Length(possibleExtension) <= MAX_FILENAME_EXTENSION_BYTES) {
      stem = name.slice(0, dot);
      extension = possibleExtension;
    }
  }
  let budget = MAX_FILENAME_UTF8_BYTES - utf8Length(suffix) - utf8Length(extension);
  if (budget < 1) {
    extension = '';
    budget = MAX_FILENAME_UTF8_BYTES - utf8Length(suffix);
  }
  stem = truncateUTF8(stem, budget).replace(/[ .]+$/u, '');
  if (/^\p{White_Space}*$/u.test(stem) || stem === '.' || stem === '..') {
    stem = 'file';
  }
  return `${stem}${suffix}${extension}`;
}

function truncateUTF8(value, maxBytes) {
  let out = '';
  for (const character of value) {
    if (utf8Length(out) + utf8Length(character) > maxBytes) {
      break;
    }
    out += character;
  }
  return out;
}

function utf8Length(value) {
  return new TextEncoder().encode(value).byteLength;
}

function foldManifestName(name) {
  return name.normalize('NFC').toLowerCase();
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
  if (!value || value.includes('=') || !BASE64URL_PATTERN.test(value)) {
    return new Uint8Array();
  }
  try {
    const padded = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(value.length / 4) * 4, '=');
    const binary = atob(padded);
    const decoded = Uint8Array.from(binary, (char) => char.charCodeAt(0));
    return encodeBase64URL(decoded) === value ? decoded : new Uint8Array();
  } catch (_error) {
    return new Uint8Array();
  }
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

function showSelectionError(message) {
  statusBox.className = 'status error';
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
