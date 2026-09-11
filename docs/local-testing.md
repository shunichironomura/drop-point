# Local testing with Python receiver/sender scripts

The `scripts/` directory contains two small Python clients that exercise the real HTTP API and protocol:

- `scripts/droppoint-receiver.py`: creates a reusable drop point, saves receiver state, and lists/picks up/decrypts/acknowledges child submissions.
- `scripts/droppoint-sender.py`: reads a full drop link, encrypts local files with the URL-fragment public key, and submits one immutable encrypted child.

They are `uv` scripts, so dependencies are installed automatically from their inline metadata.

## 1. Create a local config and API token

```sh
mkdir -p .local/local-test
cat > .local/local-test/config.json <<EOF
{
  "listen_addr": "127.0.0.1:18080",
  "base_url": "http://127.0.0.1:18080",
  "data_dir": ".local/local-test/data",
  "default_ttl_seconds": 600,
  "max_ttl_seconds": 900,
  "default_max_bytes": 52428800,
  "max_bytes": 52428800,
  "default_max_active_drop_points": 3
}
EOF

TOKEN_OUT=$(go run ./cmd/droppoint token add --id local-test --config .local/local-test/config.json)
API_TOKEN=$(printf '%s\n' "$TOKEN_OUT" | awk '/^api_token:/ {print $2}')
```

## 2. Start the relay

```sh
go run ./cmd/droppoint serve --config .local/local-test/config.json
```

In another terminal:

```sh
curl http://127.0.0.1:18080/health
```

## Browser end-to-end test on the same PC

To exercise the real browser sender page, browser-side encryption, and file upload flow,
use `localhost`. Browsers treat `http://localhost` as a secure context, so WebCrypto is
available without setting up HTTPS certificates.

Create a browser test config and save the plaintext API token separately:

```sh
mkdir -p .local/browser-test
cat > .local/browser-test/config.json <<EOF
{
  "listen_addr": "127.0.0.1:18080",
  "base_url": "http://localhost:18080",
  "data_dir": ".local/browser-test/data",
  "default_ttl_seconds": 600,
  "max_ttl_seconds": 900,
  "default_max_bytes": 52428800,
  "max_bytes": 52428800,
  "default_max_active_drop_points": 3
}
EOF

TOKEN_OUT=$(go run ./cmd/droppoint token add --id browser-test --config .local/browser-test/config.json)
API_TOKEN=$(printf '%s\n' "$TOKEN_OUT" | awk '/^api_token:/ {print $2}')
printf '%s\n' "$API_TOKEN" > .local/browser-test/api-token.txt
chmod 600 .local/browser-test/config.json .local/browser-test/api-token.txt
```

Start the relay:

```sh
go run ./cmd/droppoint serve --config .local/browser-test/config.json
```

In another terminal, create a drop point and print its drop name plus a full sender link:

```sh
./scripts/droppoint-receiver.py create \
  --base-url http://localhost:18080 \
  --api-token "$(tr -d '\n' < .local/browser-test/api-token.txt)" \
  --state .local/browser-test/state.json
```

Open the printed `http://localhost:18080/drop/...#v=2&pk=...&exp=...` link in your browser,
verify that the page shows the printed drop name, choose files, and submit the drop. Then pick up and decrypt the uploaded files:

```sh
./scripts/droppoint-receiver.py pickup \
  --state .local/browser-test/state.json \
  --out-dir .local/browser-test/output \
  --wait
```

The decrypted files are atomically published in an owner-only bundle directory under `.local/browser-test/output`. A durable per-submission receipt lets a retry verify the same bundle and safely resume acknowledgement. The page remains open and can send more bundles through the same drop link.

## 3. Receiver: create a drop point

```sh
./scripts/droppoint-receiver.py create \
  --base-url http://127.0.0.1:18080 \
  --api-token "$API_TOKEN" \
  --state .local/local-test/state.json
```

The script prints the drop name and a full sender link containing `#v=2&pk=...&exp=...`.

## 4. Sender: encrypt and drop files

```sh
mkdir -p .local/local-test/input
printf 'hello from sender\n' > .local/local-test/input/hello.txt

./scripts/droppoint-sender.py '<paste-full-drop-link-here>' \
  .local/local-test/input/hello.txt
```

The sender script encrypts the manifest and payload locally and uploads only ciphertext.

## 5. Receiver: list, pickup, decrypt, and acknowledge

```sh
./scripts/droppoint-receiver.py status \
  --state .local/local-test/state.json

./scripts/droppoint-receiver.py pickup \
  --state .local/local-test/state.json \
  --out-dir .local/local-test/output \
  --wait
```

The decrypted file is atomically published in a `bundle-dp_...-sub_...` directory under `.local/local-test/output` together with a durable identity receipt.

Pickup processes every ready child, atomically publishes each bundle under a directory named for its drop point and submission IDs, durably records the receipt, and then acknowledges that child. If interrupted, rerunning the command verifies installed receipts and resumes incomplete acknowledgements without overwriting files. The reusable parent remains open, so run the sender and pickup commands again to test additional submissions without creating or scanning another link.

Close the session explicitly when it should stop accepting files:

```sh
./scripts/droppoint-receiver.py close \
  --state .local/local-test/state.json
```

Successful close removes `recipient_private_key` from the private state file.
