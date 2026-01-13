#!/bin/bash
desc='脚本说明：
1: 同步 arm、amd 镜像 (Docker Hub -> Harbor) [注意：Docker命令需配置 daemon.json 信任非安全仓库]
2: 导出镜像到本地 (从远程地址 -> 本地 OCI 目录)
3: 导入镜像到 docker-daemon (本地 OCI 目录 -> Docker)
4: 导出镜像到本地 (指定私有仓库 -> 本地 OCI 目录)
5: 导入镜像到镜像仓库 (本地 OCI 目录 -> 目标仓库，保留原完整路径)
6: 同步amd64镜像到harbor (修复仅amd64的情况)
7: 导入镜像到 containerd (本地 OCI 目录 -> k8s/ctr)
8: 导入镜像到镜像仓库 (本地 OCI 目录 -> 目标仓库，自动去除原域名)
9: 导出镜像到本地 (从 docker-daemon -> 本地 OCI 目录) [新增]
10: 导出镜像到本地 (从 containerd -> 本地 OCI 目录) [新增]

使用示例：
bash -x image-tools.sh 1
bash -x image-tools.sh 2 arm64
bash -x image-tools.sh 3 arm64
bash -x image-tools.sh 4 harbor.senses-ai.com arm64
bash -x image-tools.sh 5 sealos.hub:5000 arm64
bash -x image-tools.sh 6
bash -x image-tools.sh 7 k8s.io arm64
bash -x image-tools.sh 8 sealos.hub:5000 arm64
bash -x image-tools.sh 9 arm64
bash -x image-tools.sh 10 k8s.io arm64
'

# 检查是否提供了位置参数
if [ $# -eq 0 ]; then
  echo "$desc"
  exit 1
fi

set -e

# 镜像列表文件
image_file="image.txt"

# 全局 TLS 忽略参数 (用于 skopeo)
TLS_FLAGS="--src-tls-verify=false --dest-tls-verify=false"

# --- 现有功能函数 ---

sync_images() {
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    echo ">>> 处理镜像: $image"
    dest_project=${1:-base}
    src_image="docker.1ms.run/${image}"
    dest_image="harbor.senses-ai.com/${dest_project}/${image}"

    docker pull --platform linux/amd64 "$src_image"
    docker tag "$src_image" "${dest_image}-amd64"
    docker push "${dest_image}-amd64"

    docker pull --platform linux/arm64 "$src_image"
    docker tag "$src_image" "${dest_image}-arm64"
    docker push "${dest_image}-arm64"

    docker manifest create --amend "$dest_image" "${dest_image}-amd64" "${dest_image}-arm64"
    docker manifest push "$dest_image"
    echo ">>> ✅ 完成镜像: $image"
  done < "$image_file"
}

export_images() {
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    arch=${1:-amd64}
    skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
        ${TLS_FLAGS} "docker://${image}" "oci:data:${image}"
  done < "$image_file"
}

import_images() {
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    arch=${1:-amd64}
    skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
        ${TLS_FLAGS} "oci:data:${image}" "docker-daemon:${image}"
  done < "$image_file"
}

export_images_2() {
  src_repository=${1:-harbor.senses-ai.com}
  arch=${2:-amd64}
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
        ${TLS_FLAGS} "docker://${src_repository}/${image}" "oci:data:${image}"
  done < "$image_file"
}

import_images_2() {
  dest_repository=${1:-sealos.hub:5000}
  arch=${2:-amd64}
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
        ${TLS_FLAGS} "oci:data:${image}" "docker://${dest_repository}/${image}"
  done < "$image_file"
}

rsync_amd64() {
  dest_repository=${1:-harbor.senses-ai.com/base}
  arch=${2:-amd64}
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
        ${TLS_FLAGS} "docker://harbor.senses-ai.com/docker-hub/${image}" "docker://${dest_repository}/${image}"
  done < "$image_file"
}

import_to_containerd() {
  namespace=${1:-k8s.io}
  arch=${2:-amd64}
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    tmp_tar="/tmp/import_ctr_${RANDOM}.tar"
    if skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
       "oci:data:${image}" "docker-archive:${tmp_tar}:${image}"; then
       ctr -n "$namespace" images import "$tmp_tar"
       rm -f "$tmp_tar"
    fi
  done < "$image_file"
}

import_oci_clean_push() {
  dest_repo=$1
  arch=${2:-amd64}
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    image_suffix="${image#*/}"
    [[ "$image" =~ ^[^/]+\.[^/]+/ ]] || image_suffix="$image"
    skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
      ${TLS_FLAGS} "oci:data:${image}" "docker://${dest_repo}/${image_suffix}"
  done < "$image_file"
}

# --- 新增功能函数 ---

# 功能 9: 从 docker-daemon 导出
export_from_docker_daemon() {
  arch=${1:-amd64}
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    echo "正在从 Docker 导出: $image"
    skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
        ${TLS_FLAGS} "docker-daemon:${image}" "oci:data:${image}"
    echo ">>> ✅ 成功导出到 oci:data:${image}"
  done < "$image_file"
}

# 功能 10: 从 containerd 导出
export_from_containerd() {
  namespace=${1:-k8s.io}
  arch=${2:-amd64}
  while read -r image; do
    [[ -z "$image" || "$image" =~ ^# ]] && continue
    tmp_tar="/tmp/export_ctr_${RANDOM}.tar"
    echo "正在从 Containerd ($namespace) 导出: $image"
    if ctr -n "$namespace" images export "$tmp_tar" "$image"; then
        skopeo copy --insecure-policy --multi-arch system --override-arch ${arch} --override-os linux \
            "docker-archive:${tmp_tar}" "oci:data:${image}"
        rm -f "$tmp_tar"
        echo ">>> ✅ 成功导出到 oci:data:${image}"
    else
        echo ">>> ❌ 镜像 $image 在 containerd 中未找到或导出失败"
    fi
  done < "$image_file"
}

# --- Case 逻辑 ---

case "$1" in
  1) sync_images $2 ;;
  2) export_images $2 ;;
  3) import_images $2 ;;
  4) export_images_2 $2 $3 ;;
  5) import_images_2 $2 $3 ;;
  6) rsync_amd64 $2 $3 ;;
  7) import_to_containerd $2 $3 ;;
  8) import_oci_clean_push $2 $3 ;;
  9) export_from_docker_daemon $2 ;;
  10) export_from_containerd $2 $3 ;;
  *) echo "无效参数"; echo "$desc"; exit 1 ;;
esac

