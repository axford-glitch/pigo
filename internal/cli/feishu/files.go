package feishu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smallnest/pigo/internal/feishuconnector"
)

// receiveFile downloads a Feishu image or file into the inbox directory.
// The saved name is only the base name, so a message cannot write outside
// the inbox by sending a path in file_name.
func (s *Service) receiveFile(ctx context.Context, msg feishuconnector.Message) Reply {
	if msg.File == nil || strings.TrimSpace(msg.File.Key) == "" {
		return Reply{Text: "这条消息没有可下载的文件。"}
	}
	if s.fetch == nil {
		return Reply{Text: "还不能从飞书下载文件。"}
	}
	data, headerName, err := s.fetch(ctx, msg.MessageID, msg.File.Key, msg.File.Kind)
	if err != nil {
		return Reply{Text: "下载飞书文件失败：" + err.Error()}
	}
	if len(data) > feishuconnectorMaxDownload {
		return Reply{Text: "文件超过 100MB，没有保存。"}
	}
	name := msg.File.Name
	if strings.TrimSpace(name) == "" {
		name = headerName
	}
	name = safeFileName(name, msg.File.Kind)
	dir := s.inboxDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Reply{Text: "创建收件目录失败：" + err.Error()}
	}
	path := uniquePath(dir, name)
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return Reply{Text: "保存文件失败：" + err.Error()}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Reply{Text: "保存文件失败：" + err.Error()}
	}
	return Reply{Text: fmt.Sprintf("已保存 %s（%d 字节）\n%s", filepath.Base(path), len(data), path)}
}

// sendLocal checks that path is a regular file inside the working directory
// or the inbox, then hands it to the bot to upload.
func (s *Service) sendLocal(arg string) Reply {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return Reply{Text: "用法：/send <路径> 或 发送文件 <路径>。只能发送当前工作目录里的文件。"}
	}
	path, err := s.resolveSendPath(arg)
	if err != nil {
		return Reply{Text: err.Error()}
	}
	return Reply{Text: "正在发送 " + filepath.Base(path), Files: []string{path}}
}

func (s *Service) inboxDir() string {
	if strings.TrimSpace(s.inbox) != "" {
		return s.inbox
	}
	return filepath.Join(s.workDir(), "feishu-inbox")
}

func (s *Service) workDir() string {
	if strings.TrimSpace(s.cwd) != "" {
		return s.cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func (s *Service) resolveSendPath(raw string) (string, error) {
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.workDir(), path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("路径无效：%w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("找不到文件：%s", abs)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", fmt.Errorf("无法解析符号链接：%s", abs)
		}
		abs = resolved
		info, err = os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("找不到文件：%s", abs)
		}
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("不是普通文件：%s", abs)
	}
	roots := []string{s.workDir()}
	inbox := s.inboxDir()
	if inbox != "" {
		roots = append(roots, inbox)
	}
	allowed := false
	for _, root := range roots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if withinDir(rootAbs, abs) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("只能发送当前工作目录里的文件：%s", abs)
	}
	return abs, nil
}

func withinDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func safeFileName(name, kind string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		if kind == "image" {
			return "image.png"
		}
		return "file.bin"
	}
	return name
}

func uniquePath(dir, name string) string {
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return filepath.Join(dir, stem+"-dup"+ext)
}

// feishuconnectorMaxDownload matches the connector's download cap so a fetch
// stub cannot ask the inbox to store an unbounded buffer.
const feishuconnectorMaxDownload = 100 << 20
