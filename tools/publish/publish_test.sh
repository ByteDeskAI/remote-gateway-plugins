#!/usr/bin/env bash
# shellcheck disable=SC2015  # pass/fail always return 0, so A && B || C is safe here
# Tests publish.sh against a fake Store (python3 http.server) that mirrors the
# bytedesk-store admin API: Bearer auth, metadata upsert, immutable versions
# (same bytes 200, new 201, different bytes 409) and the detail read-back.
# Run: bash tools/publish/publish_test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
FAKE_PID=""
trap '[[ -n "$FAKE_PID" ]] && kill "$FAKE_PID" 2>/dev/null; rm -rf "$T"' EXIT
TOKEN="test-token-$RANDOM$RANDOM"
fails=0
pass() { printf 'ok   %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1"; fails=$((fails + 1)); }

cat >"$T/fake.py" <<'PY'
import hashlib, json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
TOKEN = os.environ["FAKE_TOKEN"]
LIE = os.environ.get("FAKE_LIE") == "1"
pkgs = {}
log = open(sys.argv[2], "a")
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def send(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def authed(self):
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            self.send(401, {"error": "admin required"}); return False
        return True
    def body(self):
        return self.rfile.read(int(self.headers.get("Content-Length", 0)))
    def do_POST(self):
        if not self.authed(): return
        row = json.loads(self.body())
        log.write(json.dumps({"post": row}) + "\n"); log.flush()
        created = row["id"] not in pkgs
        p = pkgs.setdefault(row["id"], {"versions": [], "latestVersion": ""})
        p.update(row)
        self.send(201 if created else 200, p)
    def do_PUT(self):
        if not self.authed(): return
        path, _, q = self.path.partition("?")
        _, _, _, _, pid, _, ver, _ = path.split("/")
        raw = self.body(); sha = hashlib.sha256(raw).hexdigest()
        log.write(json.dumps({"put": path, "query": q}) + "\n"); log.flush()
        p = pkgs.get(pid)
        if p is None: return self.send(404, {"error": "publish metadata first"})
        for v in p["versions"]:
            if v["version"] == ver:
                return self.send(200, v) if v["sha256"] == sha else self.send(409, {"error": "conflict"})
        v = {"version": ver, "sha256": sha}; p["versions"].append(v)
        if "latest=1" in q: p["latestVersion"] = ver
        self.send(201, v)
    def do_GET(self):
        if not self.authed(): return
        p = pkgs.get(self.path.rsplit("/", 1)[1])
        if p is None: return self.send(404, {"error": "not found"})
        if LIE: p = dict(p, versions=[dict(v, sha256="0" * 64) for v in p["versions"]])
        self.send(200, p)
s = HTTPServer(("127.0.0.1", 0), H)
open(sys.argv[1], "w").write(str(s.server_port))
s.serve_forever()
PY

start_fake() {
  [[ -n "$FAKE_PID" ]] && kill "$FAKE_PID" 2>/dev/null && wait "$FAKE_PID" 2>/dev/null || true
  rm -f "$T/port"; : >"$T/log"
  FAKE_TOKEN="$TOKEN" FAKE_LIE="${1:-0}" python3 "$T/fake.py" "$T/port" "$T/log" &
  FAKE_PID=$!
  for _ in $(seq 50); do [[ -s "$T/port" ]] && break; sleep 0.1; done
  URL="http://127.0.0.1:$(cat "$T/port")"
}

# make_dist DIR ID VERSION PAYLOAD: one runner-shaped output directory.
make_dist() {
  local out="$1/$2" src="$T/src-$RANDOM"
  mkdir -p "$out" "$src/$2"
  jq -n --arg id "$2" --arg v "$3" '{contract: 2, kind: "process", id: $id, version: $v,
    minCoreVersion: "0.9.0", identity: {displayName: "Example", description: "Demo"},
    pricing: {model: "free"}, publisher: {id: "bytedesk"}, binary: $id}' >"$src/$2/plugin.json"
  printf '%s' "$4" >"$src/$2/$2"
  tar -C "$src" -czf "$out/$2-$3.tar.gz" "$2"
  local sha; sha="$(sha256sum "$out/$2-$3.tar.gz" | cut -d' ' -f1)"
  printf '%s  %s\n' "$sha" "$2-$3.tar.gz" >"$out/SHA256SUMS"
  jq -n --arg id "$2" --arg v "$3" --arg s "$sha" --arg f "$2-$3.tar.gz" \
    '{contractVersion: 1, id: $id, version: $v, archive: $f, sha256: $s}' >"$out/provenance.json"
}

run() { STORE_ADMIN_TOKEN="${TOK-$TOKEN}" bash "$HERE/publish.sh" --store "$URL" --dist "$1" >"$T/out" 2>&1; }

start_fake
make_dist "$T/d1" example 0.1.0-dev.7 bin-a
make_dist "$T/d1" other 1.2.3 bin-b

if run "$T/d1"; then pass "publishes two new packages"; else fail "publishes two new packages"; cat "$T/out"; fi
grep -q 'PASS example 0.1.0-dev.7 sha256 matches' "$T/out" && pass "verifies sha256 read-back" || fail "verifies sha256 read-back"
grep -q '"put": "/v1/admin/packages/example/versions/0.1.0-dev.7/artifact", "query": "latest=1"' "$T/log" \
  && pass "uploads with ?latest=1" || fail "uploads with ?latest=1"
row="$(grep -m1 '"post"' "$T/log" | jq -c '.post')"
[[ "$row" == '{"id":"example","name":"Example","description":"Demo","publisher":"bytedesk","kind":"process_plugin","category":"plugins","pricing":"free","targets":["gateway"],"min_core_version":"0.9.0"}' ]] \
  && pass "catalog row derived from plugin.json" || { fail "catalog row derived from plugin.json"; echo "$row"; }
grep -qF "$TOKEN" "$T/out" && fail "token leaked to output" || pass "token never printed"

if run "$T/d1"; then pass "identical re-upload (200) succeeds"; else fail "identical re-upload (200) succeeds"; cat "$T/out"; fi
grep -q 'already published with the same bytes' "$T/out" && pass "reports 200 as already published" || fail "reports 200 as already published"

make_dist "$T/d2" example 0.1.0-dev.7 DIFFERENT
if run "$T/d2"; then fail "different bytes (409) fails"; else grep -q 'DIFFERENT bytes' "$T/out" && pass "different bytes (409) fails" || fail "different bytes (409) message"; fi

make_dist "$T/d3" example 0.1.0-dev.8 bin-c
printf '%064d  example-0.1.0-dev.8.tar.gz\n' 0 >"$T/d3/example/SHA256SUMS"
: >"$T/log"
if run "$T/d3"; then fail "bad SHA256SUMS fails"; else pass "bad SHA256SUMS fails"; fi
[[ ! -s "$T/log" ]] && pass "nothing uploaded when verification fails" || fail "nothing uploaded when verification fails"

make_dist "$T/d4" example 0.1.0-dev.9 bin-d
jq '.version = "9.9.9"' "$T/d4/example/provenance.json" >"$T/p" && mv "$T/p" "$T/d4/example/provenance.json"
if run "$T/d4"; then fail "provenance mismatch fails"; else pass "provenance mismatch fails"; fi

if TOK="" run "$T/d1"; then fail "missing token fails"; else grep -q 'STORE_ADMIN_TOKEN is not set' "$T/out" && pass "missing token fails" || fail "missing token message"; fi
if TOK="wrong" run "$T/d1"; then fail "rejected token (401) fails"; else pass "rejected token (401) fails"; fi

start_fake 1
make_dist "$T/d5" example 0.2.0 bin-e
if run "$T/d5"; then fail "read-back sha mismatch fails"; else grep -q 'Store serves sha256' "$T/out" && pass "read-back sha mismatch fails" || fail "read-back sha mismatch message"; fi

mkdir -p "$T/empty"
if run "$T/empty"; then pass "empty dist is a no-op"; else fail "empty dist is a no-op"; fi

echo; [[ $fails -eq 0 ]] && echo "publish_test: all passed" || { echo "publish_test: $fails failed"; exit 1; }
