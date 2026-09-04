#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 vX.Y.Z SHA256 OUTPUT/comms.rb" >&2
  exit 2
}

[[ $# -eq 3 ]] || usage

version=$1
sha256=$2
output=$3

[[ $version =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo "invalid release version: $version" >&2
  exit 2
}
[[ $sha256 =~ ^[0-9a-f]{64}$ ]] || {
  echo "invalid SHA-256: $sha256" >&2
  exit 2
}
[[ $(basename "$output") == comms.rb ]] || {
  echo "formula output must be named comms.rb" >&2
  exit 2
}
[[ -d $(dirname "$output") ]] || {
  echo "formula output directory does not exist: $(dirname "$output")" >&2
  exit 2
}

temporary=$(mktemp "$(dirname "$output")/.comms.rb.XXXXXX")
cleanup() {
  rm -f "$temporary"
}
trap cleanup EXIT

cat >"$temporary" <<EOF
class Comms < Formula
  desc "Short-lived local messaging for independent agent sessions"
  homepage "https://github.com/marcus/comms"
  url "https://github.com/marcus/comms/archive/refs/tags/$version.tar.gz"
  sha256 "$sha256"
  license "Apache-2.0"
  head "https://github.com/marcus/comms.git", branch: "main"

  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "0"
    ldflags = [
      "-s",
      "-w",
      "-X github.com/marcus/comms/pkg/buildinfo.Version=$version",
      "-X github.com/marcus/comms/pkg/buildinfo.Commit=homebrew",
    ].join(" ")
    system "go", "build", *std_go_args(output: bin/"comms", ldflags:), "./cmd/comms"
  end

  service do
    run [opt_bin/"comms", "serve", "--supervised"]
    run_at_load true
    keep_alive true
    log_path var/"log/comms.log"
    error_log_path var/"log/comms.err.log"
  end

  test do
    assert_match "comms $version (homebrew)", shell_output("#{bin}/comms version")
  end
end
EOF

ruby -c "$temporary" >/dev/null
chmod 0644 "$temporary"
mv "$temporary" "$output"
trap - EXIT

echo "rendered $output for $version"
