# イメージと Release の配布物を、1 回の `docker buildx bake` で作る（理由は Dockerfile と .github/workflows/release.yml の冒頭）。
# 1 回の呼び出しの中で、Dockerfile の web と build の段は 1 回しか作られない。
# release.yml の PR では VERSION を dev-<sha> にする（既定の dev は手元用）。
# 使い方: VERSION=dev docker buildx bake   （イメージは docker に読み込み、配布物は ./dist に出す）

variable "IMAGE" {
  default = "ghcr.io/t-seki/rajikura"
}

variable "VERSION" {
  default = "dev"
}

group "default" {
  targets = ["image", "artifacts"]
}

target "_common" {
  context    = "."
  dockerfile = "Dockerfile"
  args = {
    VERSION = VERSION
  }
}

# 実行用のイメージ（Dockerfile の最後の段）
target "image" {
  inherits = ["_common"]
  tags     = ["${IMAGE}:${VERSION}"]
  output   = ["type=docker"]
}

# 配布物（Linux・Windows のバイナリ）
target "artifacts" {
  inherits = ["_common"]
  target   = "artifacts"
  output   = ["type=local,dest=dist"]
}
