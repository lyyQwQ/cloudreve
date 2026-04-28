package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/pkg/ffmpegworker"
	settingpkg "github.com/cloudreve/Cloudreve/v4/pkg/setting"
	"github.com/gofrs/uuid"
)

var remoteWorkerHTTPClient = http.DefaultClient

type remoteWorkerJobCreateResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

type remoteWorkerJobStatus struct {
	JobID             string  `json:"job_id"`
	Status            string  `json:"status"`
	Progress          float64 `json:"progress"`
	DownloadProgress  float64 `json:"download_progress"`
	TranscodeProgress float64 `json:"transcode_progress"`
	Error             string  `json:"error"`
	StderrTail        string  `json:"stderr_tail"`
	OutputSize        int64   `json:"output_size"`
	DownloadedBytes   int64   `json:"downloaded_bytes"`
	TotalBytes        int64   `json:"total_bytes"`
}

func loadRemoteFFMpegWorkerConfig(ctx context.Context) *settingpkg.RemoteFFMpegWorker {
	dep, ok := depFromContext(ctx).(interface{ SettingProvider() settingpkg.Provider })
	if !ok {
		return &settingpkg.RemoteFFMpegWorker{}
	}

	cfg := dep.SettingProvider().RemoteFFMpegWorker(ctx)
	if cfg == nil {
		return &settingpkg.RemoteFFMpegWorker{}
	}
	return cfg
}

func shouldUseRemoteSubtitleBurn(input string, option *VideoSubtitleOption, modeUsed string, cfg *settingpkg.RemoteFFMpegWorker) bool {
	if cfg == nil || !cfg.Enabled || cfg.Endpoint == "" || cfg.APIKey == "" {
		return false
	}
	if modeUsed != VideoSubtitleModeEmbedded {
		return false
	}
	if option == nil || option.EmbeddedIndex == nil || *option.EmbeddedIndex < 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(option.Mode), VideoSubtitleModeEmbedded)
}

func remoteSubtitleEmbeddedIndex(option *VideoSubtitleOption) int {
	if option != nil && option.EmbeddedIndex != nil && *option.EmbeddedIndex >= 0 {
		return *option.EmbeddedIndex
	}
	return 0
}

func buildRemoteFFMpegWorkerSourceURL(ctx context.Context, taskRef *VideoSubtitleBurnTask, fileModel *ent.File, cfg *settingpkg.RemoteFFMpegWorker) (string, error) {
	if taskRef == nil || fileModel == nil {
		return "", fmt.Errorf("invalid remote worker source url argument (%w)", CriticalErr)
	}

	dep, ok := depFromContext(ctx).(interface{ SettingProvider() settingpkg.Provider })
	if !ok {
		return "", fmt.Errorf("missing setting provider for remote worker (%w)", CriticalErr)
	}

	siteURL := dep.SettingProvider().SiteURL(settingpkg.UseFirstSiteUrl(ctx))
	if siteURL == nil || siteURL.Scheme == "" || siteURL.Host == "" {
		return "", fmt.Errorf("site url is required for remote worker source url (%w)", CriticalErr)
	}
	if fileModel.PrimaryEntity <= 0 {
		return "", fmt.Errorf("file has no primary entity (%w)", CriticalErr)
	}

	nonce := uuid.Must(uuid.NewV4()).String()
	ttl := cfg.SourceURLTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	claims := ffmpegworker.SourceURLClaims{
		TaskID:   taskRef.ID(),
		FileID:   fileModel.ID,
		EntityID: fileModel.PrimaryEntity,
		Expires:  time.Now().Add(ttl).Unix(),
		Nonce:    nonce,
	}
	sourcePath := fmt.Sprintf("%s/video/worker/source/%d", constants.APIPrefix, taskRef.ID())
	return ffmpegworker.BuildSourceURL(siteURL, sourcePath, claims, dep.SettingProvider().SecretKey(ctx))
}

func runRemoteSubtitleBurn(ctx context.Context, taskRef *VideoSubtitleBurnTask, cfg *settingpkg.RemoteFFMpegWorker, sourceURL string, embeddedIndex int, duration float64, bitrate int, outputPath string) error {
	workerCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		workerCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()

	jobID, err := createRemoteWorkerJob(workerCtx, cfg, sourceURL, embeddedIndex, duration, bitrate)
	if err != nil {
		return err
	}
	updateVideoTaskWorkerJob(taskRef.DBTask, jobID)

	completed := false
	defer func() {
		if !completed && workerCtx.Err() != nil {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = cancelRemoteWorkerJob(cancelCtx, cfg, jobID)
		}
	}()

	status, err := pollRemoteWorkerJob(workerCtx, taskRef, cfg, jobID)
	if err != nil {
		return err
	}
	if status.OutputSize > 0 {
		updateVideoTaskWorkerOutputSize(taskRef.DBTask, status.OutputSize)
	}

	if err := downloadRemoteWorkerOutput(workerCtx, taskRef, cfg, jobID, outputPath, status.OutputSize); err != nil {
		return err
	}
	completed = true
	updateVideoTaskWorkerProgress(taskRef.DBTask, workerTransferPhaseOutputDownload, 100, 100, 0, 0)
	updateVideoTaskWorkerOutputSize(taskRef.DBTask, status.OutputSize)
	return nil
}

func createRemoteWorkerJob(ctx context.Context, cfg *settingpkg.RemoteFFMpegWorker, sourceURL string, embeddedIndex int, duration float64, bitrate int) (string, error) {
	payload := map[string]any{
		"source_url":     sourceURL,
		"embedded_index": embeddedIndex,
		"duration":       duration,
		"bitrate":        bitrate,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(cfg.Endpoint, "/") + "/v1/jobs/embedded-subtitle-burn-url"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := remoteWorkerHTTPClient.Do(req)
	if err != nil {
		return "", redactRemoteWorkerErr(cfg, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := readRemoteWorkerErrorBody(resp.Body, cfg)
		return "", fmt.Errorf("remote worker create job failed: status=%d body=%s", resp.StatusCode, msg)
	}

	var created remoteWorkerJobCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("failed to decode remote worker create response: %w", err)
	}
	created.JobID = strings.TrimSpace(created.JobID)
	if created.JobID == "" {
		return "", fmt.Errorf("remote worker create response missing job_id")
	}
	return created.JobID, nil
}

func pollRemoteWorkerJob(ctx context.Context, taskRef *VideoSubtitleBurnTask, cfg *settingpkg.RemoteFFMpegWorker, jobID string) (*remoteWorkerJobStatus, error) {
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}

	for {
		status, err := getRemoteWorkerJob(ctx, cfg, jobID)
		if err != nil {
			return nil, err
		}
		updateVideoTaskWorkerStatus(taskRef.DBTask, status)

		switch strings.ToLower(strings.TrimSpace(status.Status)) {
		case "queued", "running":
		case "completed":
			updateVideoTaskWorkerProgress(taskRef.DBTask, workerTransferPhaseSourceDownload, 100, 100, status.DownloadedBytes, status.TotalBytes)
			updateVideoTaskFFMpegProgress(taskRef.DBTask, 100)
			return status, nil
		case "failed", "canceled":
			msg := strings.TrimSpace(status.Error)
			if msg == "" {
				msg = strings.TrimSpace(status.StderrTail)
			}
			return nil, fmt.Errorf("remote worker job %s: %s", status.Status, redactRemoteWorkerText(cfg, msg))
		default:
			return nil, fmt.Errorf("remote worker returned unknown status %q", status.Status)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func getRemoteWorkerJob(ctx context.Context, cfg *settingpkg.RemoteFFMpegWorker, jobID string) (*remoteWorkerJobStatus, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/") + "/v1/jobs/" + url.PathEscape(jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := remoteWorkerHTTPClient.Do(req)
	if err != nil {
		return nil, redactRemoteWorkerErr(cfg, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := readRemoteWorkerErrorBody(resp.Body, cfg)
		return nil, fmt.Errorf("remote worker poll failed: status=%d body=%s", resp.StatusCode, msg)
	}

	var status remoteWorkerJobStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode remote worker status: %w", err)
	}
	return &status, nil
}

func cancelRemoteWorkerJob(ctx context.Context, cfg *settingpkg.RemoteFFMpegWorker, jobID string) error {
	endpoint := strings.TrimRight(cfg.Endpoint, "/") + "/v1/jobs/" + url.PathEscape(jobID) + "/cancel"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := remoteWorkerHTTPClient.Do(req)
	if err != nil {
		return redactRemoteWorkerErr(cfg, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote worker cancel failed: status=%d", resp.StatusCode)
	}
	return nil
}

func downloadRemoteWorkerOutput(ctx context.Context, taskRef *VideoSubtitleBurnTask, cfg *settingpkg.RemoteFFMpegWorker, jobID, outputPath string, expectedSize int64) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create remote worker output dir: %w", err)
	}

	headSize, err := headRemoteWorkerOutput(ctx, cfg, jobID)
	if err != nil {
		return err
	}
	if expectedSize <= 0 {
		expectedSize = headSize
	}
	if expectedSize > 0 && headSize > 0 && expectedSize != headSize {
		return fmt.Errorf("remote worker output size mismatch: worker=%d head=%d", expectedSize, headSize)
	}

	partPath := outputPath + ".part"
	offset := int64(0)
	if st, err := os.Stat(partPath); err == nil {
		offset = st.Size()
		if expectedSize > 0 && offset > expectedSize {
			if removeErr := os.Remove(partPath); removeErr != nil {
				return fmt.Errorf("failed to reset oversized remote worker part file: %w", removeErr)
			}
			offset = 0
		}
	}
	if expectedSize > 0 && offset == expectedSize {
		if err := os.Rename(partPath, outputPath); err != nil {
			return fmt.Errorf("failed to finalize existing remote worker part file: %w", err)
		}
		return nil
	}

	endpoint := strings.TrimRight(cfg.Endpoint, "/") + "/v1/jobs/" + url.PathEscape(jobID) + "/output"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := remoteWorkerHTTPClient.Do(req)
	if err != nil {
		return redactRemoteWorkerErr(cfg, err)
	}
	defer resp.Body.Close()

	if offset > 0 && resp.StatusCode == http.StatusOK {
		offset = 0
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		msg := readRemoteWorkerErrorBody(resp.Body, cfg)
		return fmt.Errorf("remote worker output download failed: status=%d body=%s", resp.StatusCode, msg)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 && resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}
	part, err := os.OpenFile(partPath, flags, 0600)
	if err != nil {
		return fmt.Errorf("failed to open remote worker part file: %w", err)
	}

	w := &remoteWorkerProgressWriter{
		taskRef:      taskRef,
		phase:        workerTransferPhaseOutputDownload,
		current:      offset,
		expectedSize: expectedSize,
	}
	_, copyErr := io.Copy(part, io.TeeReader(resp.Body, w))
	syncErr := part.Sync()
	closeErr := part.Close()
	if copyErr != nil {
		return fmt.Errorf("failed to download remote worker output: %w", copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("failed to sync remote worker part file: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close remote worker part file: %w", closeErr)
	}

	st, err := os.Stat(partPath)
	if err != nil {
		return fmt.Errorf("failed to stat remote worker part file: %w", err)
	}
	if expectedSize > 0 && st.Size() != expectedSize {
		return fmt.Errorf("remote worker output size mismatch after download: want=%d got=%d", expectedSize, st.Size())
	}
	if err := os.Rename(partPath, outputPath); err != nil {
		return fmt.Errorf("failed to finalize remote worker output: %w", err)
	}
	return nil
}

func headRemoteWorkerOutput(ctx context.Context, cfg *settingpkg.RemoteFFMpegWorker, jobID string) (int64, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/") + "/v1/jobs/" + url.PathEscape(jobID) + "/output"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := remoteWorkerHTTPClient.Do(req)
	if err != nil {
		return 0, redactRemoteWorkerErr(cfg, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("remote worker output head failed: status=%d", resp.StatusCode)
	}
	return resp.ContentLength, nil
}

type remoteWorkerProgressWriter struct {
	taskRef      *VideoSubtitleBurnTask
	phase        string
	current      int64
	expectedSize int64
}

func (w *remoteWorkerProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.current += int64(n)
	progress := float64(0)
	if w.expectedSize > 0 {
		progress = float64(w.current) / float64(w.expectedSize) * 100
	}
	updateVideoTaskWorkerProgress(w.taskRef.DBTask, w.phase, progress, 100, w.current, w.expectedSize)
	return n, nil
}

func updateVideoTaskWorkerJob(taskRef *DBTask, jobID string) {
	state, err := ParseVideoTaskState(taskRef.State())
	if err != nil {
		return
	}
	state.WorkerJobID = jobID
	persistVideoTaskState(taskRef, state)
}

func updateVideoTaskWorkerStatus(taskRef *DBTask, status *remoteWorkerJobStatus) {
	if status == nil {
		return
	}
	download := status.DownloadProgress
	if download <= 0 && status.DownloadedBytes > 0 && status.TotalBytes > 0 {
		download = float64(status.DownloadedBytes) / float64(status.TotalBytes) * 100
	}
	transcode := status.TranscodeProgress
	if transcode <= 0 {
		transcode = status.Progress
	}
	updateVideoTaskWorkerProgress(taskRef, workerTransferPhaseSourceDownload, download, transcode, status.DownloadedBytes, status.TotalBytes)
	if status.OutputSize > 0 {
		updateVideoTaskWorkerOutputSize(taskRef, status.OutputSize)
	}
}

func updateVideoTaskWorkerProgress(taskRef *DBTask, phase string, transferPct, transcodePct float64, downloadedBytes, totalBytes int64) {
	if taskRef == nil {
		return
	}

	state, err := ParseVideoTaskState(taskRef.State())
	if err != nil {
		return
	}

	state.WorkerTransferPhase = phase
	state.WorkerTransferProgress = clampFFMpegProgress(transferPct)
	state.WorkerTranscodeProgress = clampFFMpegProgress(transcodePct)
	state.FFmpegProgress = state.WorkerTranscodeProgress
	if downloadedBytes > 0 {
		state.WorkerDownloadedBytes = downloadedBytes
	}
	if totalBytes > 0 {
		state.WorkerTotalBytes = totalBytes
	}
	persistVideoTaskState(taskRef, state)
}

func updateVideoTaskWorkerOutputSize(taskRef *DBTask, outputSize int64) {
	if taskRef == nil || outputSize <= 0 {
		return
	}

	state, err := ParseVideoTaskState(taskRef.State())
	if err != nil {
		return
	}
	state.WorkerOutputSize = outputSize
	persistVideoTaskState(taskRef, state)
}

func readRemoteWorkerErrorBody(r io.Reader, cfg *settingpkg.RemoteFFMpegWorker) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	return redactRemoteWorkerText(cfg, string(b))
}

func redactRemoteWorkerErr(cfg *settingpkg.RemoteFFMpegWorker, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", redactRemoteWorkerText(cfg, err.Error()))
}

func redactRemoteWorkerText(cfg *settingpkg.RemoteFFMpegWorker, text string) string {
	if cfg != nil && cfg.APIKey != "" {
		text = strings.ReplaceAll(text, cfg.APIKey, "REDACTED")
	}
	text = regexp.MustCompile(`(?i)(Authorization:\s*Bearer\s+)[^\s]+`).ReplaceAllString(text, "${1}REDACTED")
	text = regexp.MustCompile(`(?i)(https?://[^\s"']+/api/v4/video/worker/source/[^?\s"']+\?)[^\s"']+`).ReplaceAllString(text, "${1}REDACTED")
	text = regexp.MustCompile(`(?i)(/api/v4/video/worker/source/[^?\s"']+\?)[^\s"']+`).ReplaceAllString(text, "${1}REDACTED")
	text = regexp.MustCompile(`(?i)(signature=)[^&\s]+`).ReplaceAllString(text, "${1}REDACTED")
	return strings.TrimSpace(text)
}
