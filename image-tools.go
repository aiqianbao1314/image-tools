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

//go:embed bin/skopeo
var skopeoFS embed.FS

// 定义最终存放 skopeo 的位置
const targetSkopeoPath = "/bin/skopeo"

func ensureSkopeoExists() error {
	// 1. 检查文件是否已经存在
	if _, err := os.Stat(targetSkopeoPath); err == nil {
		// 文件已存在，直接返回
		return nil
	}

	// 2. 如果不存在，从 embed 中读取
	fmt.Printf("未发现 %s，正在初始化内嵌的 skopeo...\n", targetSkopeoPath)
	data, err := skopeoFS.ReadFile("bin/skopeo")
	if err != nil {
		return fmt.Errorf("读取内嵌文件失败: %v", err)
	}

	// 3. 确保目标目录存在 (针对 /bin 之外的目录，比如 ./bin)
	targetDir := filepath.Dir(targetSkopeoPath)
	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return fmt.Errorf("创建目录失败: %v", err)
		}
	}

	// 4. 写入二进制文件并赋予执行权限
	// 注意：如果目标是 /bin，这里可能会因为权限不足报错
	err = os.WriteFile(targetSkopeoPath, data, 0755)
	if err != nil {
		return fmt.Errorf("写入文件失败 (请检查是否有权限): %v", err)
	}

	fmt.Println("skopeo 初始化完成")
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

const (
	imageFile = "image.txt"
	tlsFlags  = "--src-tls-verify=false --dest-tls-verify=false"
)

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
		srcRepo := getArg(args, 0, "harbor.senses-ai.com")
		arch := getArg(args, 1, "amd64")
		runForEachImage(func(image string) { exportImages2(image, srcRepo, arch) })
	case "5":
		destRepo := getArg(args, 0, "sealos.hub:5000")
		arch := getArg(args, 1, "amd64")
		runForEachImage(func(image string) { importImages2(image, destRepo, arch) })
	case "6":
		destRepo := getArg(args, 0, "harbor.senses-ai.com/base")
		arch := getArg(args, 1, "amd64")
		runForEachImage(func(image string) { rsyncAmd64(image, destRepo, arch) })
	case "7":
		ns := getArg(args, 0, "k8s.io")
		arch := getArg(args, 1, "amd64")
		runForEachImage(func(image string) { importToContainerd(image, ns, arch) })
	case "8":
		destRepo := getArg(args, 0, "sealos.hub:5000")
		arch := getArg(args, 1, "amd64")
		runForEachImage(func(image string) { importOciCleanPush(image, destRepo, arch) })
	case "9":
		arch := getArg(args, 0, "amd64")
		runForEachImage(func(image string) { exportFromDockerDaemon(image, arch) })
	case "10":
		ns := getArg(args, 0, "k8s.io")
		arch := getArg(args, 1, "amd64")
		runForEachImage(func(image string) { exportFromContainerd(image, ns, arch) })
	case "11":
		// 直接调用，无需处理 args
		compressFiles()
	default:
		fmt.Printf("无效参数: %s\n", command)
		printUsage()
	}
}

// --- 核心功能函数 ---

func syncImages(image, destProject string) {
	srcImage := "docker.1ms.run/" + image
	destBase := fmt.Sprintf("harbor.senses-ai.com/%s/%s", destProject, image)

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

func skopeoCopy(src, dest, arch string) {
	args := []string{
		"copy", "--insecure-policy", "--multi-arch", "system",
		"--override-arch", arch, "--override-os", "linux",
		"--src-tls-verify=false", "--dest-tls-verify=false",
		src, dest,
	}
	execute("skopeo", args...)
}

func exportImages(image, arch string) {
	skopeoCopy("docker://"+image, "oci:data:"+image, arch)
}

func importImages(image, arch string) {
	skopeoCopy("oci:data:"+image, "docker-daemon:"+image, arch)
}

func exportImages2(image, srcRepo, arch string) {
	skopeoCopy(fmt.Sprintf("docker://%s/%s", srcRepo, image), "oci:data:"+image, arch)
}

func importImages2(image, destRepo, arch string) {
	skopeoCopy("oci:data:"+image, fmt.Sprintf("docker://%s/%s", destRepo, image), arch)
}

func rsyncAmd64(image, destRepo, arch string) {
	skopeoCopy("docker://harbor.senses-ai.com/docker-hub/"+image, "docker://"+destRepo+"/"+image, arch)
}

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

func exportFromDockerDaemon(image, arch string) {
	skopeoCopy("docker-daemon:"+image, "oci:data:"+image, arch)
}

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
%s 1
%s 2 arm64
%s 3 arm64
%s 4 harbor.senses-ai.com arm64
%s 5 sealos.hub:5000 arm64
%s 6
%s 7 k8s.io arm64
%s 8 sealos.hub:5000 arm64
%s 9 arm64
%s 10 k8s.io arm64

`, exeName, exeName, exeName, exeName, exeName, exeName, exeName, exeName, exeName, exeName)
}
