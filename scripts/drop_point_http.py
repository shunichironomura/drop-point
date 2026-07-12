from __future__ import annotations

import json
import re
from dataclasses import dataclass
from urllib import error, parse, request

_CAPABILITY_RE = re.compile(r"(?:drop|pick|api)_[A-Za-z0-9_-]+")


@dataclass(frozen=True)
class HTTPResponse:
    content_type: str
    body: bytes


def request_bytes(
    method: str,
    url: str,
    *,
    user_agent: str,
    bearer_token: str | None = None,
    body: bytes | None = None,
    headers: dict[str, str] | None = None,
    timeout: float = 30,
) -> HTTPResponse:
    all_headers = {"User-Agent": user_agent}
    all_headers.update(headers or {})
    if bearer_token:
        all_headers["Authorization"] = "Bearer " + bearer_token
    http_request = request.Request(url, data=body, headers=all_headers, method=method)
    try:
        with request.urlopen(http_request, timeout=timeout) as response:  # noqa: S310 - caller supplies the relay URL.
            return HTTPResponse(response.headers.get("Content-Type", ""), response.read())
    except error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        try:
            detail = json.dumps(json.loads(detail), indent=2)
        except json.JSONDecodeError:
            pass
        safe_url = redact_capability_url(exc.geturl() or url)
        safe_detail = redact_capabilities(detail)
        raise RuntimeError(f"HTTP {exc.code} from {safe_url}: {safe_detail}") from exc
    except error.URLError as exc:
        raise RuntimeError(f"request to {redact_capability_url(url)} failed: {redact_capabilities(str(exc.reason))}") from exc


def redact_capability_url(url: str) -> str:
    parsed = parse.urlparse(url)
    redacted_path = redact_capabilities(parsed.path)
    parts = redacted_path.split("/")
    if len(parts) >= 4 and parts[-3:-1] == ["api", "drops"]:
        parts[-1] = ":drop_token"
    redacted_query = redact_capabilities(parsed.query)
    rendered = parse.urlunparse((parsed.scheme, parsed.netloc, "/".join(parts), "", redacted_query, ""))
    return redact_capabilities(rendered)


def redact_capabilities(value: str) -> str:
    return _CAPABILITY_RE.sub(":capability", value)
