const { test } = require('node:test');
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const vm = require('node:vm');
const { webcrypto } = require('node:crypto');

class Element {
  children = [];
  disabled = false;
  hidden = true;
  textContent = '';
  classList = { add() {}, remove() {}, toggle() {} };
  addEventListener() {}
  setAttribute() {}
  append(...children) { this.children.push(...children); }
  prepend(child) { child.parent = this; this.children.unshift(child); }
  replaceChildren(...children) { this.children = children; }
  get lastElementChild() { return this.children.at(-1); }
  remove() { this.parent.children.pop(); }
}

function page() {
  const elements = new Map();
  const context = vm.createContext({
    TextEncoder, TextDecoder, Uint8Array, Blob, FormData, URL, Date,
    crypto: webcrypto, btoa, atob,
    location: { pathname: '/drop/drop_test', hash: '#v=2&pk=B6N8vBQgk8i3VdwbEOhstCY3StFqqFPtC9_AsrhtHHw' },
    URLSearchParams,
    document: {
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, new Element());
        return elements.get(id);
      },
      createElement() { return new Element(); },
    },
    window: { isSecureContext: true, addEventListener() {}, setInterval() {}, clearInterval() {} },
    // Leave initialization pending; tests set authoritative metadata directly.
    fetch: () => new Promise(() => {}),
  });
  vm.runInContext(readFileSync(`${__dirname}/app.js`, 'utf8'), context);
  vm.runInContext(`
    state.displayName = 'calm-otter';
    state.ready = true;
    state.expiresAt = new Date(Date.now() + 600000);
    state.maxBytes = 10000;
    buildEncryptedBundle = async () => ({ envelope: { test: true }, encryptedPayload: new Uint8Array([1, 2, 3]) });
  `, context);
  return {
    context, elements,
    run: (code) => vm.runInContext(code, context),
    select: () => vm.runInContext("setSelectedFiles([{ name: 'notes.txt', type: 'text/plain', size: 3 }])", context),
  };
}

test('success keeps the session usable and each batch gets a new ID', async () => {
  const p = page();
  const paths = [];
  p.context.fetch = async (path) => { paths.push(path); return { ok: true }; };
  for (let i = 0; i < 2; i++) {
    p.select();
    await p.run('dropSelectedFiles()');
    assert.equal(p.elements.get('files').disabled, false);
    assert.equal(p.run('state.selectedFiles.length'), 0);
    assert.equal(p.run('state.pendingBundle'), null);
  }
  assert.notEqual(paths[0], paths[1]);
  assert.equal(p.elements.get('sent-count').textContent, '2');
  assert.equal(p.elements.get('sent-submissions').children.length, 2);
});

test('queue full and lost response retain the ID and exact ciphertext for retry', async () => {
  for (const failure of [429, 'network']) {
    const p = page();
    const requests = [];
    p.context.fetch = async (path, request) => {
      requests.push({ path, bytes: await request.body.get('payload').text() });
      if (requests.length === 1) {
        if (failure === 'network') throw new Error('connection lost');
        return { ok: false, status: failure };
      }
      return { ok: true };
    };
    p.select();
    await assert.rejects(p.run('dropSelectedFiles()'));
    assert.equal(p.elements.get('submit').textContent, 'Retry send');
    assert.equal(p.run('state.selectedFiles.length'), 1);
    // A retry must not encrypt again.
    p.run("buildEncryptedBundle = async () => { throw new Error('unexpected encryption'); }");
    await p.run('dropSelectedFiles()');
    assert.deepEqual(requests[0], requests[1]);
    assert.equal(p.run('state.sentCount'), 1);
  }
});

test('double send and selection edits cannot race an in-flight batch', async () => {
  const p = page();
  let finish;
  let calls = 0;
  p.context.fetch = () => { calls++; return new Promise((resolve) => { finish = resolve; }); };
  p.select();
  const sending = p.run('dropSelectedFiles()');
  await new Promise(setImmediate);
  await p.run('dropSelectedFiles()');
  p.run('setSelectedFiles([])');
  assert.equal(calls, 1);
  assert.equal(p.run('state.selectedFiles.length'), 1);
  finish({ ok: true });
  await sending;
});

test('expiry while sending cannot re-enable the page after success', async () => {
  const p = page();
  let finish;
  p.context.fetch = () => new Promise((resolve) => { finish = resolve; });
  p.select();
  const sending = p.run('dropSelectedFiles()');
  await new Promise(setImmediate);
  p.run("showError('This drop point has expired.')");
  finish({ ok: true });
  await sending;
  assert.equal(p.elements.get('files').disabled, true);
  assert.equal(p.elements.get('submit').disabled, true);
  assert.equal(p.run('state.sentCount'), 1);
});

test('editing a failed selection clears its retry identity and cached payload', async () => {
  const p = page();
  p.context.fetch = async () => ({ ok: false, status: 429 });
  p.select();
  await assert.rejects(p.run('dropSelectedFiles()'));
  p.run('removeSelectedFile(0)');
  assert.equal(p.run('state.pendingSubmissionID'), null);
  assert.equal(p.run('state.pendingBundle'), null);
  assert.equal(p.elements.get('submit').disabled, true);
});

test('closed sessions disable controls, hide the countdown, and retain sent history', async () => {
  const p = page();
  p.run("recordSentSubmission([{ name: 'earlier.txt' }])");
  p.context.fetch = async () => ({ ok: false, status: 410 });
  p.select();
  await assert.rejects(p.run('dropSelectedFiles()'));
  assert.equal(p.elements.get('files').disabled, true);
  assert.equal(p.elements.get('submit').disabled, true);
  assert.equal(p.elements.get('expiry').hidden, true);
  assert.equal(p.elements.get('sent-count').textContent, '1');
  assert.equal(p.run('state.pendingBundle'), null);
});

test('recent send history is bounded and rendered as text', () => {
  const p = page();
  for (let i = 0; i < 25; i++) p.run("recordSentSubmission([{ name: '<script>not markup</script>' }])");
  assert.equal(p.elements.get('sent-submissions').children.length, 20);
  assert.equal(p.elements.get('sent-count').textContent, '25');
  assert.equal(p.elements.get('sent-submissions').children[0].children[1].textContent, '<script>not markup</script>');
});

function photo(p) {
  return p.run(`cameraInput.files = [Object.assign(new Blob(['photo'], {type: 'image/jpeg'}), {name: 'photo.jpg'})]; capturePhoto()`);
}

test('camera mode automatically sends successive confirmed photos, not cancelled pickers', async () => {
  const p = page();
  let calls = 0;
  p.context.fetch = async () => { calls++; return { ok: true }; };
  p.run('setCameraMode(true)');
  await p.run('cameraInput.files = []; capturePhoto()');
  assert.equal(calls, 0);
  for (let i = 0; i < 2; i++) {
    await photo(p);
    assert.equal(p.elements.get('camera').disabled, false);
    assert.equal(p.elements.get('camera').value, '');
    assert.equal(p.elements.get('submit').hidden, true);
    assert.equal(p.elements.get('files').disabled, true);
  }
  assert.equal(calls, 2);
  assert.equal(p.run('state.sentCount'), 2);
  p.run('setCameraMode(false)');
  assert.equal(p.elements.get('files').disabled, false);
});

test('failed camera sends retain the photo for retry and cannot be replaced by another capture', async () => {
  const p = page();
  p.context.fetch = async () => ({ ok: false, status: 429 });
  p.run('setCameraMode(true)');
  await photo(p);
  const id = p.run('state.pendingSubmissionID');
  assert.equal(p.elements.get('camera').disabled, true);
  assert.equal(p.elements.get('files-mode').disabled, true);
  assert.equal(p.elements.get('submit').textContent, 'Retry send');
  await photo(p);
  assert.equal(p.run('state.pendingSubmissionID'), id);
  p.run('setCameraMode(false)');
  assert.equal(p.run('state.cameraMode'), true);
  p.context.fetch = async () => ({ ok: true });
  await p.run('sendSelectedFiles()');
  assert.equal(p.elements.get('camera').disabled, false);
  assert.equal(p.run('state.sentCount'), 1);
});

test('oversized camera photos are retained for removal, not sent', async () => {
  const p = page();
  let calls = 0;
  p.context.fetch = async () => { calls++; return { ok: true }; };
  p.run('state.maxBytes = 16; setCameraMode(true)');
  await photo(p);
  assert.equal(calls, 0);
  assert.equal(p.elements.get('camera').disabled, true);
  p.run('removeSelectedFile(0)');
  assert.equal(p.elements.get('camera').disabled, false);
});

test('camera callback after expiry or outside camera mode cannot send', async () => {
  const p = page();
  let calls = 0;
  p.context.fetch = async () => { calls++; return { ok: true }; };
  await photo(p);
  p.run('setCameraMode(true); state.expiresAt = new Date(0)');
  await photo(p);
  assert.equal(calls, 0);
  assert.equal(p.elements.get('camera').disabled, true);
  assert.equal(p.elements.get('camera-mode').disabled, true);
});
