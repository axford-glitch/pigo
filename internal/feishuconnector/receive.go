package feishuconnector

import (
	"context"
	"fmt"
	"io"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// maxDownload is the Feishu message-resource limit: the API rejects anything
// larger, and we refuse to buffer more than that in memory.
const maxDownload = 100 << 20

// DownloadResource fetches one image or file that arrived on a message.
// kind is the resource type the API expects: "image" or "file". The returned
// name comes from the response headers and may be empty.
func (b *Bot) DownloadResource(ctx context.Context, messageID, fileKey, kind string) ([]byte, string, error) {
	messageID = strings.TrimSpace(messageID)
	fileKey = strings.TrimSpace(fileKey)
	kind = strings.TrimSpace(kind)
	if messageID == "" || fileKey == "" {
		return nil, "", fmt.Errorf("feishu download: message id and file key are required")
	}
	if kind == "" {
		kind = "file"
	}
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type(kind).
		Build()
	resp, err := b.sdkClient().Im.MessageResource.Get(ctx, req)
	if err != nil {
		return nil, "", err
	}
	if resp == nil || resp.File == nil {
		if resp != nil && !resp.Success() {
			return nil, "", fmt.Errorf("feishu download: %s", feishuCodeError(resp.Code, resp.Msg))
		}
		return nil, "", fmt.Errorf("feishu download: empty file")
	}
	data, err := io.ReadAll(io.LimitReader(resp.File, maxDownload+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxDownload {
		return nil, "", fmt.Errorf("feishu download: file exceeds %d bytes", maxDownload)
	}
	return data, resp.FileName, nil
}
