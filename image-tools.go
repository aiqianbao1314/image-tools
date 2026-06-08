// Package main 提供容器镜像管理工具 image-tools，支持在 Docker/OCI 镜像仓库、
// Docker Daemon、Containerd 和本地 OCI 目录之间传输镜像。
//
// 所有镜像传输操作均从当前目录的 image.txt 文件读取镜像列表，每行一个镜像地址，
// 支持 # 开头的注释行和空行。请确保运行前 image.txt 文件存在且内容正确。
package main

import (
	"bufio"
	"embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 默认仓库地址常量，可通过命令行参数覆盖。
const (
	defaultSrcRegistry  = "docker.1ms.run"             // Docker Hub 代理源
	defaultDestRegistry = "harbor.zs.shaipower.online" // 默认目标 Harbor 仓库
	defaultDestRepo     = "sealos.hub:5000"            // 默认目标仓库地址
	defaultContainerdNS = "k8s.io"                     // 默认 Containerd 命名空间
	defaultArch         = "amd64"                      // 默认 CPU 架构
)

const (
	imageFile = "image.txt"
)

//go:embed bin/skopeo
var skopeoFS embed.FS

// ensureSkopeoExists 检查 skopeo 是否可用：
// 1. 已在 PATH 中 -> 仅确保补全配置
// 2. PATH 中没有 -> 直接解压内嵌二进制（离线部署）
func ensureSkopeoExists() {
	if _, err := exec.LookPath("skopeo"); err == nil {
		ensureShellCompletion()
		return
	}

	fmt.Println(">>> 检测到 skopeo 未安装，正在自动安装...")
	fmt.Println(">>> 离线部署模式：将使用内嵌 skopeo 二进制...")
	if err := installEmbeddedSkopeo(); err != nil {
		fmt.Printf(">>> ❌ skopeo 初始化失败: %v\n请手动安装: https://github.com/containers/skopeo/blob/main/install.md\n", err)
		os.Exit(1)
	}
	fmt.Println(">>> ✅ skopeo 初始化成功（内嵌版本）")
	ensureShellCompletion()
}

// installEmbeddedSkopeo 将内嵌二进制写到 /usr/local/bin 或 ~/.local/bin。
func installEmbeddedSkopeo() error {
	data, err := skopeoFS.ReadFile("bin/skopeo")
	if err != nil {
		return fmt.Errorf("读取内嵌 skopeo 失败: %v", err)
	}

	candidates := []string{"/usr/local/bin/skopeo"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "skopeo"))
	}

	for _, target := range candidates {
		dir := filepath.Dir(target)
		if err := os.MkdirAll(dir, 0755); err != nil {
			continue
		}
		if err := os.WriteFile(target, data, 0755); err != nil {
			continue
		}
		fmt.Printf(">>> skopeo 已安装到 %s\n", target)
		// 若写入用户目录，更新当前进程 PATH 并提示永久配置
		if !strings.HasPrefix(target, "/usr") {
			os.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			fmt.Printf(">>> ⚠️  请将 %s 永久加入 PATH，例如: echo 'export PATH=%s:$PATH' >> ~/.bashrc\n", dir, dir)
		}
		return nil
	}
	return fmt.Errorf("无法写入任何目标路径，请以 root 权限重试或手动安装")
}

// ensureShellCompletion 首次运行时为当前 shell 配置 skopeo 命令补全。
func ensureShellCompletion() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	markerDir := filepath.Join(home, ".image-tools")
	markerFile := filepath.Join(markerDir, ".skopeo-completion-done")

	// 标记文件存在则跳过，避免每次启动重复执行
	if _, err := os.Stat(markerFile); err == nil {
		return
	}

	fmt.Println(">>> 正在设置 skopeo 命令补全（仅首次执行）...")

	shell := filepath.Base(os.Getenv("SHELL"))
	var setupErr error
	switch shell {
	case "bash":
		setupErr = setupBashCompletion(home)
	case "zsh":
		setupErr = setupZshCompletion(home)
	case "fish":
		setupErr = setupFishCompletion(home)
	default:
		fmt.Printf(">>> ⚠️  未识别的 shell: %s，跳过补全设置\n", shell)
		return
	}

	if setupErr != nil {
		fmt.Printf(">>> ⚠️  补全设置失败: %v\n", setupErr)
		return
	}

	os.MkdirAll(markerDir, 0755)
	os.WriteFile(markerFile, []byte("done\n"), 0644)
}

func setupBashCompletion(home string) error {
	// 通过 source <(skopeo completion bash) 方式启用补全，追加到 ~/.bashrc
	bashrc := filepath.Join(home, ".bashrc")
	const completionCmd = "source <(skopeo completion bash)"
	const snippet = "\n# skopeo shell completion\n" + completionCmd + "\n"

	existing, _ := os.ReadFile(bashrc)
	if strings.Contains(string(existing), completionCmd) {
		fmt.Println(">>> ℹ️ bash 补全命令已存在于 ~/.bashrc，跳过追加")
		return nil
	}

	f, err := os.OpenFile(bashrc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("写入 ~/.bashrc 失败: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(snippet); err != nil {
		return fmt.Errorf("写入补全命令失败: %v", err)
	}

	fmt.Printf(">>> ✅ 已向 ~/.bashrc 追加补全命令: %s\n", completionCmd)
	fmt.Println(">>> 请执行: source ~/.bashrc")
	return nil
}

func setupZshCompletion(home string) error {
	script, err := exec.Command("skopeo", "completion", "zsh").Output()
	if err != nil {
		return fmt.Errorf("获取 zsh 补全脚本失败: %v", err)
	}
	compDir := filepath.Join(home, ".zsh", "completions")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(compDir, "_skopeo"), script, 0644); err != nil {
		return fmt.Errorf("写入补全文件失败: %v", err)
	}
	zshrc := filepath.Join(home, ".zshrc")
	fpathSnippet := fmt.Sprintf("\n# skopeo shell completion\nfpath=(%s $fpath)\nautoload -Uz compinit && compinit\n", compDir)
	if existing, _ := os.ReadFile(zshrc); !strings.Contains(string(existing), compDir) {
		f, err := os.OpenFile(zshrc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("写入 ~/.zshrc 失败: %v", err)
		}
		defer f.Close()
		f.WriteString(fpathSnippet)
	}
	fmt.Printf(">>> ✅ zsh 补全已安装到 %s/_skopeo，请执行: source ~/.zshrc\n", compDir)
	return nil
}

func setupFishCompletion(home string) error {
	script, err := exec.Command("skopeo", "completion", "fish").Output()
	if err != nil {
		return fmt.Errorf("获取 fish 补全脚本失败: %v", err)
	}
	compDir := filepath.Join(home, ".config", "fish", "completions")
	if err := os.MkdirAll(compDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %v", err)
	}
	compFile := filepath.Join(compDir, "skopeo.fish")
	if err := os.WriteFile(compFile, script, 0644); err != nil {
		return fmt.Errorf("写入补全文件失败: %v", err)
	}
	fmt.Printf(">>> ✅ fish 补全已安装到 %s\n", compFile)
	return nil
}

func compressFiles() {
	targets := []string{"data", "image-tools", "image.txt"}
	outputFile := "images_package.tar.gz"

	// 1. 检查哪些目标确实存在
	var existingTargets []string
	for _, t := range targets {
		if _, err := os.Stat(t); err == nil {
			existingTargets = append(existingTargets, t)
		}
	}

	if len(existingTargets) == 0 {
		fmt.Println(">>> ❌ 错误: 没有任何有效文件可供压缩！")
		return
	}

	// --- ⏳ 开始计时 ---
	startTime := time.Now()

	// 2. 检查系统是否安装了 pigz
	_, err := exec.LookPath("pigz")
	hasPigz := err == nil

	if hasPigz {
		// --- 方案 A: 使用 pigz 加速 ---
		fmt.Printf(">>> 📦 发现 pigz，正在加速压缩: %v -> [%s]\n", existingTargets, outputFile)

		tarCmd := exec.Command("tar", append([]string{"-cf", "-"}, existingTargets...)...)
		pigzCmd := exec.Command("pigz")

		outFile, _ := os.Create(outputFile)
		defer outFile.Close()

		pigzCmd.Stdin, _ = tarCmd.StdoutPipe()
		pigzCmd.Stdout = outFile
		pigzCmd.Stderr = os.Stderr

		pigzCmd.Start()
		tarCmd.Run()
		if err := pigzCmd.Wait(); err != nil {
			fmt.Printf(">>> ❌ pigz 压缩失败: %v\n", err)
			return
		}
	} else {
		// --- 方案 B: 自动降级使用标准 tar ---
		fmt.Printf(">>> ⚠️ 未发现 pigz，已自动降级为标准 tar 压缩: %v -> [%s]\n", existingTargets, outputFile)

		tarArgs := append([]string{"-czf", outputFile}, existingTargets...)
		err := execute("tar", tarArgs...)
		if err != nil {
			fmt.Printf(">>> ❌ tar 压缩失败: %v\n", err)
			return
		}
	}

	// --- ⌛ 结束计时并打印结果 ---
	duration := time.Since(startTime)

	fmt.Println("---")
	fmt.Printf(">>> ✅ 压缩完成！\n")
	fmt.Printf(">>> 📂 生成文件: %s\n", outputFile)
	fmt.Printf(">>> ⏱️  任务耗时: %v\n", duration.Round(time.Second)) // 四舍五入到秒，更直观
	fmt.Println("---")
}

func main() {
	ensureSkopeoExists()

	if len(os.Args) < 2 {
		printUsage()
		return
	}

	command := os.Args[1]
	args := os.Args[2:]

	switch command {
	case "1":
		destProject := "base"
		if len(args) > 0 {
			destProject = args[0]
		}
		runForEachImage(func(image string) { syncImages(image, destProject) })
	case "2":
		arch := getArg(args, 0, "amd64")
		runForEachImage(func(image string) { exportImages(image, arch) })
	case "3":
		arch := getArg(args, 0, "amd64")
		runForEachImage(func(image string) { importImages(image, arch) })
	case "4":
		srcRepo := getArg(args, 0, defaultDestRegistry)
		arch := getArg(args, 1, defaultArch)
		runForEachImage(func(image string) { exportImages2(image, srcRepo, arch) })
	case "5":
		destRepo := getArg(args, 0, defaultDestRepo)
		arch := getArg(args, 1, defaultArch)
		runForEachImage(func(image string) { importImages2(image, destRepo, arch) })
	case "6":
		destRepo := getArg(args, 0, defaultDestRegistry+"/base")
		arch := getArg(args, 1, defaultArch)
		runForEachImage(func(image string) { rsyncAmd64(image, destRepo, arch) })
	case "7":
		ns := getArg(args, 0, defaultContainerdNS)
		arch := getArg(args, 1, defaultArch)
		runForEachImage(func(image string) { importToContainerd(image, ns, arch) })
	case "8":
		destRepo := getArg(args, 0, defaultDestRepo)
		arch := getArg(args, 1, defaultArch)
		runForEachImage(func(image string) { importOciCleanPush(image, destRepo, arch) })
	case "9":
		arch := getArg(args, 0, defaultArch)
		runForEachImage(func(image string) { exportFromDockerDaemon(image, arch) })
	case "10":
		ns := getArg(args, 0, defaultContainerdNS)
		arch := getArg(args, 1, defaultArch)
		runForEachImage(func(image string) { exportFromContainerd(image, ns, arch) })
	case "11":
		// 直接调用，无需处理 args
		compressFiles()
	case "12":
		destRepo := getArg(args, 0, defaultDestRegistry+"/base")
		arch := getArg(args, 1, defaultArch)
		runForEachImage(func(image string) { pullAndPush(image, destRepo, arch) })
	default:
		fmt.Printf("无效参数: %s\n", command)
		printUsage()
	}
}

// --- 核心功能函数 ---

// syncImages 从 Docker Hub 代理源拉取镜像的 amd64/arm64 两个架构版本，
// 分别 tag 为 {image}-amd64 和 {image}-arm64 后推送到 Harbor，最后创建并推送多架构 manifest。
func syncImages(image, destProject string) {
	srcImage := defaultSrcRegistry + "/" + image
	destBase := fmt.Sprintf("%s/%s/%s", defaultDestRegistry, destProject, image)

	platforms := []string{"amd64", "arm64"}
	for _, arch := range platforms {
		tagged := fmt.Sprintf("%s-%s", destBase, arch)
		execute("docker", "pull", "--platform", "linux/"+arch, srcImage)
		execute("docker", "tag", srcImage, tagged)
		execute("docker", "push", tagged)
	}

	execute("docker", "manifest", "create", "--amend", destBase, destBase+"-amd64", destBase+"-arm64")
	execute("docker", "manifest", "push", destBase)
}

// skopeoCopy 使用 skopeo 在传输协议之间复制镜像（docker://、oci:、docker-daemon:、docker-archive: 等）。
func skopeoCopy(src, dest, arch string) {
	args := []string{
		"copy", "--insecure-policy", "--multi-arch", "system",
		"--override-arch", arch, "--override-os", "linux",
		"--src-tls-verify=false", "--dest-tls-verify=false",
		src, dest,
	}
	execute("skopeo", args...)
}

// exportImages 将远程镜像导出到本地 OCI 目录（docker:// -> oci:data:）。
func exportImages(image, arch string) {
	skopeoCopy("docker://"+image, "oci:data:"+image, arch)
}

// importImages 将本地 OCI 目录中的镜像导入到 Docker Daemon（oci:data: -> docker-daemon:）。
func importImages(image, arch string) {
	skopeoCopy("oci:data:"+image, "docker-daemon:"+image, arch)
}

// exportImages2 将指定私有仓库的镜像导出到本地 OCI 目录。
func exportImages2(image, srcRepo, arch string) {
	skopeoCopy(fmt.Sprintf("docker://%s/%s", srcRepo, image), "oci:data:"+image, arch)
}

// importImages2 将本地 OCI 目录中的镜像推送到目标仓库（保留原完整路径）。
func importImages2(image, destRepo, arch string) {
	skopeoCopy("oci:data:"+image, fmt.Sprintf("docker://%s/%s", destRepo, image), arch)
}

// rsyncAmd64 将指定架构的镜像从 harbor.senses-ai.com/docker-hub/ 同步到目标仓库。
func rsyncAmd64(image, destRepo, arch string) {
	skopeoCopy("docker://"+defaultDestRegistry+"/docker-hub/"+image, "docker://"+destRepo+"/"+image, arch)
}

// importToContainerd 将本地 OCI 目录中的镜像导出为 tar 后导入到 Containerd 的指定命名空间。
func importToContainerd(image, ns, arch string) {
	tmpTar := fmt.Sprintf("/tmp/import_ctr_%s.tar", randomString(5))
	defer os.Remove(tmpTar)

	err := execute("skopeo", "copy", "--insecure-policy", "--multi-arch", "system",
		"--override-arch", arch, "--override-os", "linux",
		"oci:data:"+image, "docker-archive:"+tmpTar+":"+image)

	if err == nil {
		execute("ctr", "-n", ns, "images", "import", tmpTar)
	}
}

// importOciCleanPush 将本地 OCI 目录中的镜像推送到目标仓库，并自动去除镜像名中的原始域名。
func importOciCleanPush(image, destRepo, arch string) {
	// 模拟 Bash 中的 ${image#*/} 逻辑，去除域名
	imageSuffix := image
	if strings.Contains(image, ".") && strings.Contains(image, "/") {
		parts := strings.SplitN(image, "/", 2)
		if len(parts) > 1 {
			imageSuffix = parts[1]
		}
	}
	skopeoCopy("oci:data:"+image, fmt.Sprintf("docker://%s/%s", destRepo, imageSuffix), arch)
}

// pullAndPush 先将镜像下载到本地 OCI 目录，再上传到目标仓库。
// 若镜像名已包含域名（如 docker.io/library/nginx），目标路径会自动去除原始域名。
func pullAndPush(image, destRepo, arch string) {
	// 去除原始域名，保留路径作为目标镜像名
	imagePath := image
	if idx := strings.Index(image, "/"); idx != -1 {
		host := image[:idx]
		if strings.ContainsAny(host, ".:'") || host == "localhost" {
			imagePath = image[idx+1:]
		}
	}

	// 第一步：下载到本地 OCI 目录
	fmt.Printf(">>> [1/2] 下载: docker://%s -> oci:data:%s\n", image, image)
	err := execute("skopeo", "copy", "--insecure-policy", "--multi-arch", "system",
		"--override-arch", arch, "--override-os", "linux",
		"--src-tls-verify=false", "--dest-tls-verify=false",
		"docker://"+image, "oci:data:"+image)
	if err != nil {
		log.Printf(">>> ❌ 下载失败: %s: %v", image, err)
		return
	}

	// 第二步：从本地 OCI 目录上传到目标仓库
	dest := fmt.Sprintf("docker://%s/%s", destRepo, imagePath)
	fmt.Printf(">>> [2/2] 上传: oci:data:%s -> %s\n", image, dest)
	execute("skopeo", "copy", "--insecure-policy", "--multi-arch", "system",
		"--override-arch", arch, "--override-os", "linux",
		"--src-tls-verify=false", "--dest-tls-verify=false",
		"oci:data:"+image, dest)
}

// exportFromDockerDaemon 从 Docker Daemon 导出镜像到本地 OCI 目录（docker-daemon: -> oci:data:）。
func exportFromDockerDaemon(image, arch string) {
	skopeoCopy("docker-daemon:"+image, "oci:data:"+image, arch)
}

// exportFromContainerd 从 Containerd 的指定命名空间导出镜像到本地 OCI 目录。
func exportFromContainerd(image, ns, arch string) {
	tmpTar := fmt.Sprintf("/tmp/export_ctr_%s.tar", randomString(5))
	defer os.Remove(tmpTar)

	err := execute("ctr", "-n", ns, "images", "export", tmpTar, image)
	if err == nil {
		skopeoCopy("docker-archive:"+tmpTar, "oci:data:"+image, arch)
	} else {
		log.Printf(">>> ❌ 镜像 %s 在 containerd 中未找到", image)
	}
}

// --- 辅助工具函数 ---

func execute(name string, args ...string) error {
	fmt.Printf("执行: %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runForEachImage(fn func(string)) {
	file, err := os.Open(imageFile)
	if err != nil {
		log.Fatalf("无法打开文件 %s: %v", imageFile, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fn(line)
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("读取文件 %s 时出错: %v", imageFile, err)
	}
}

func getArg(args []string, index int, defaultValue string) string {
	if index < len(args) {
		return args[index]
	}
	return defaultValue
}

func randomString(n int) string {
	return fmt.Sprintf("%d", os.Getpid()) // 简单用进程PID模拟随机
}

func printUsage() {
	// 1. 获取用户执行时的原始命令（包含相对路径，如 ./my-tool）
	rawPath := os.Args[0]

	// 2. 仅获取二进制文件名（如 my-tool）
	exeName := filepath.Base(rawPath)

	fmt.Printf(`使用说明:

	本工具所有镜像传输操作均从当前目录的 %s 文件读取镜像列表。
	每行一个镜像地址，支持 # 开头的注释行和空行。
	请确保运行前 image.txt 文件存在且内容正确。

	命令列表:
	1: 同步 arm、amd 镜像 (Docker Hub -> Harbor) [注意：Docker命令需配置 daemon.json 信任非安全仓库]
	2: 导出镜像到本地 (从远程地址 -> 本地 OCI 目录)
	3: 导入镜像到 docker-daemon (本地 OCI 目录 -> Docker)
	4: 导出镜像到本地 (指定私有仓库 -> 本地 OCI 目录)
	5: 导入镜像到镜像仓库 (本地 OCI 目录 -> 目标仓库，保留原完整路径)
	6: 同步amd64镜像到harbor (修复仅amd64的情况)
	7: 导入镜像到 containerd (本地 OCI 目录 -> k8s/ctr)
	8: 导入镜像到镜像仓库 (本地 OCI 目录 -> 目标仓库，自动去除原域名)
	9: 导出镜像到本地 (从 docker-daemon -> 本地 OCI 目录)
	10: 导出镜像到本地 (从 containerd -> 本地 OCI 目录)
	11: 打包压缩 (将 data/、image-tools、image.txt 压缩为 images_package.tar.gz)
	12: 拉取并上传 (源镜像 -> 本地 OCI 目录 -> 目标仓库，自动去除原域名)

	默认仓库地址（可通过命令行参数覆盖）:
	  源仓库: %s
	  目标 Harbor: %s
	  目标仓库: %s
	  Containerd 命名空间: %s

	使用示例：
	%s 1
	%s 2 arm64
	%s 3 arm64
	%s 4 %s amd64
	%s 5 %s amd64
	%s 6
	%s 7 %s amd64
	%s 8 %s amd64
	%s 9 amd64
	%s 10 %s amd64
	%s 11
	%s 12 %s/base amd64

	`, imageFile, defaultSrcRegistry, defaultDestRegistry, defaultDestRepo, defaultContainerdNS,
		exeName, exeName, exeName,
		exeName, defaultDestRegistry,
		exeName, defaultDestRepo,
		exeName,
		exeName, defaultContainerdNS,
		exeName, defaultDestRepo,
		exeName, exeName, defaultContainerdNS,
		exeName,
		exeName, defaultDestRegistry)
}
