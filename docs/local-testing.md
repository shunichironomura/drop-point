# Local testing with Python receiver/sender scripts

The `scripts/` directory contains two small Python clients that exercise the real HTTP API and protocol:

- `scripts/drop-point-receiver.py`: creates a drop point, saves receiver state, polls/picks up/decrypts, and closes.
- `scripts/drop-point-sender.py`: reads a full drop link, encrypts local files with the URL-fragment public key, and submits one encrypted drop.

They are `uv` scripts, so dependencies are installed automatically from their inline metadata.

## 1. Create a local config with one API token

```sh
mkdir -p .local/local-test
TOKEN_OUT=$(go run ./cmd/drop-point token generate)
API_TOKEN=$(printf '%s\n' "$TOKEN_OUT" | awk '/^api_token:/ {print $2}')
SECRET_HASH=$(printf '%s\n' "$TOKEN_OUT" | awk '/^secret_hash:/ {print $2}')

cat > .local/local-test/config.json <<EOF
{
  "listen_addr": "127.0.0.1:18080",
  "base_url": "http://127.0.0.1:18080",
  "data_dir": ".local/local-test/data",
  "default_ttl_seconds": 600,
  "max_ttl_seconds": 900,
  "default_max_bytes": 52428800,
  "max_bytes": 52428800,
  "default_max_active_drop_points": 3,
  "api_tokens": [
    {"id":"local-test","secret_hash":"$SECRET_HASH","enabled":true}
  ]
}
EOF
```

## 2. Start the relay

```sh
go run ./cmd/drop-point serve --config .local/local-test/config.json
```

In another terminal:

```sh
curl http://127.0.0.1:18080/health
```

## 3. Receiver: create a drop point

```sh
./scripts/drop-point-receiver.py create \
  --base-url http://127.0.0.1:18080 \
  --api-token "$API_TOKEN" \
  --state .local/local-test/state.json
```

The script prints a full sender link containing `#v=2&pk=...`.

## 4. Sender: encrypt and drop files

```sh
mkdir -p .local/local-test/input
printf 'hello from sender\n' > .local/local-test/input/hello.txt

./scripts/drop-point-sender.py '<paste-full-drop-link-here>' \
  .local/local-test/input/hello.txt
```

The sender script encrypts the manifest and payload locally and uploads only ciphertext.

## 5. Receiver: status, pickup, decrypt, close

```sh
./scripts/drop-point-receiver.py status \
  --state .local/local-test/state.json

./scripts/drop-point-receiver.py pickup \
  --state .local/local-test/state.json \
  --out-dir .local/local-test/output \
  --wait
```

By default pickup closes the remote drop point after decrypted files are written locally. Use `--no-close` to keep it open for repeated pickup testing.
