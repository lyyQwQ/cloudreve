package video

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/logging"
	"github.com/cloudreve/Cloudreve/v4/pkg/queue"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

const (
	batchVideoMaxFiles = 100

	batchRowStatusReady        = "ready"
	batchRowStatusSkipped      = "skipped"
	batchRowStatusConflict     = "conflict"
	batchRowStatusFailed       = "failed"
	batchRowStatusExisting     = "existing"
	batchRowStatusProcessing   = "processing"
	batchRowStatusIncompatible = "incompatible"

	batchCandidateTypeExternal = "external"
	batchCandidateTypeEmbedded = "embedded"
)

type batchFileIDsRequest struct {
	FileIDs []fileIDRaw `json:"file_ids" binding:"required"`
}

type batchSubtitleBurnRequest struct {
	FileIDs      []fileIDRaw `json:"file_ids" binding:"required"`
	CandidateKey string      `json:"candidate_key" binding:"required"`
}

type batchSubtitlePreflightResponse struct {
	Candidates []batchSubtitleCandidate `json:"candidates"`
	Rows       []batchSubtitleRow       `json:"rows"`
	Summary    batchResultSummary       `json:"summary"`
}

type batchSubtitleCandidate struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type batchSubtitleRow struct {
	FileID     string                       `json:"file_id"`
	FileName   string                       `json:"file_name"`
	Status     string                       `json:"status"`
	Reason     string                       `json:"reason,omitempty"`
	Candidates []batchSubtitleCandidateBind `json:"candidates,omitempty"`
}

type batchSubtitleCandidateBind struct {
	CandidateKey  string `json:"candidate_key"`
	Type          string `json:"type"`
	ExternalName  string `json:"external_name,omitempty"`
	EmbeddedIndex *int   `json:"embedded_index,omitempty"`
	Label         string `json:"label"`
}

type batchCreateResponse struct {
	Rows    []batchCreateRow   `json:"rows"`
	Summary batchResultSummary `json:"summary"`
}

type batchCreateRow struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
}

type batchHLSPreflightResponse struct {
	Rows    []batchHLSRow      `json:"rows"`
	Summary batchResultSummary `json:"summary"`
}

type batchHLSRow struct {
	FileID     string `json:"file_id"`
	FileName   string `json:"file_name"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Codec      string `json:"codec,omitempty"`
	AudioCodec string `json:"audio_codec,omitempty"`
}

type batchResultSummary struct {
	Ready        int `json:"ready,omitempty"`
	Created      int `json:"created,omitempty"`
	Skipped      int `json:"skipped,omitempty"`
	Conflict     int `json:"conflict,omitempty"`
	Failed       int `json:"failed,omitempty"`
	Existing     int `json:"existing,omitempty"`
	Processing   int `json:"processing,omitempty"`
	Incompatible int `json:"incompatible,omitempty"`
}

func BatchSubtitlePreflight(c *gin.Context) {
	var req batchFileIDsRequest
	if !bindBatchRequest(c, &req.FileIDs, func() error { return c.ShouldBindJSON(&req) }) {
		return
	}

	resp := buildBatchSubtitlePreflight(c, req.FileIDs)
	c.JSON(http.StatusOK, serializer.Response{Code: 0, Data: resp})
}

func BatchSubtitleBurn(c *gin.Context) {
	var req batchSubtitleBurnRequest
	if !bindBatchRequest(c, &req.FileIDs, func() error { return c.ShouldBindJSON(&req) }) {
		return
	}

	candidateKey := strings.TrimSpace(req.CandidateKey)
	if candidateKey == "" {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "candidate_key is required"})
		return
	}

	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, serializer.Response{Code: 1, Msg: "unauthorized"})
		return
	}

	preflight := buildBatchSubtitlePreflight(c, req.FileIDs)
	rows := make([]batchCreateRow, 0, len(preflight.Rows))
	for _, row := range preflight.Rows {
		createRow := batchCreateRow{FileID: row.FileID, FileName: row.FileName}
		if row.Status != batchRowStatusReady {
			createRow.Status = batchRowStatusSkipped
			createRow.Reason = row.Reason
			if createRow.Reason == "" {
				createRow.Reason = "not ready"
			}
			rows = append(rows, createRow)
			continue
		}

		binding, ok := findSubtitleBinding(row.Candidates, candidateKey)
		if !ok {
			createRow.Status = batchRowStatusSkipped
			createRow.Reason = "selected candidate is not available for this file"
			rows = append(rows, createRow)
			continue
		}

		fileID, err := resolveFileID(dep, row.FileID)
		if err != nil {
			createRow.Status = batchRowStatusFailed
			createRow.Reason = err.Error()
			rows = append(rows, createRow)
			continue
		}

		option := subtitleOptionFromBinding(binding)
		taskID, status, reason := createSubtitleTaskForBatch(c, dep, user, fileID, option)
		createRow.Status = status
		createRow.Reason = reason
		createRow.TaskID = taskID
		rows = append(rows, createRow)
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Data: batchCreateResponse{Rows: rows, Summary: summarizeCreateRows(rows)}})
}

func BatchHLSPreflight(c *gin.Context) {
	var req batchFileIDsRequest
	if !bindBatchRequest(c, &req.FileIDs, func() error { return c.ShouldBindJSON(&req) }) {
		return
	}

	resp := buildBatchHLSPreflight(c, req.FileIDs)
	c.JSON(http.StatusOK, serializer.Response{Code: 0, Data: resp})
}

func BatchHLS(c *gin.Context) {
	var req batchFileIDsRequest
	if !bindBatchRequest(c, &req.FileIDs, func() error { return c.ShouldBindJSON(&req) }) {
		return
	}

	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, serializer.Response{Code: 1, Msg: "unauthorized"})
		return
	}

	preflight := buildBatchHLSPreflight(c, req.FileIDs)
	rows := make([]batchCreateRow, 0, len(preflight.Rows))
	for _, row := range preflight.Rows {
		createRow := batchCreateRow{FileID: row.FileID, FileName: row.FileName}
		if row.Status != batchRowStatusReady {
			createRow.Status = batchRowStatusSkipped
			createRow.Reason = row.Reason
			if createRow.Reason == "" {
				createRow.Reason = row.Status
			}
			rows = append(rows, createRow)
			continue
		}

		fileID, err := resolveFileID(dep, row.FileID)
		if err != nil {
			createRow.Status = batchRowStatusFailed
			createRow.Reason = err.Error()
			rows = append(rows, createRow)
			continue
		}

		taskID, status, reason := createHLSTaskForBatch(c, dep, user, fileID)
		createRow.Status = status
		createRow.Reason = reason
		createRow.TaskID = taskID
		rows = append(rows, createRow)
	}

	c.JSON(http.StatusOK, serializer.Response{Code: 0, Data: batchCreateResponse{Rows: rows, Summary: summarizeCreateRows(rows)}})
}

func bindBatchRequest(c *gin.Context, fileIDs *[]fileIDRaw, bind func() error) bool {
	if err := bind(); err != nil {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "bad request", Error: err.Error()})
		return false
	}
	if len(*fileIDs) == 0 {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: "file_ids is required"})
		return false
	}
	if len(*fileIDs) > batchVideoMaxFiles {
		c.JSON(http.StatusBadRequest, serializer.Response{Code: 1, Msg: fmt.Sprintf("too many files, max %d", batchVideoMaxFiles)})
		return false
	}
	return true
}

func buildBatchSubtitlePreflight(c *gin.Context, rawIDs []fileIDRaw) batchSubtitlePreflightResponse {
	dep := dependency.FromContext(c)
	rows := make([]batchSubtitleRow, 0, len(rawIDs))
	candidateCounts := make(map[string]int)
	candidateMeta := make(map[string]batchSubtitleCandidate)

	for _, rawID := range rawIDs {
		row := inspectSubtitleBatchFile(c, dep, string(rawID))
		if row.Status == batchRowStatusReady {
			seen := make(map[string]bool)
			for _, candidate := range row.Candidates {
				if seen[candidate.CandidateKey] {
					continue
				}
				seen[candidate.CandidateKey] = true
				candidateCounts[candidate.CandidateKey]++
				candidateMeta[candidate.CandidateKey] = batchSubtitleCandidate{
					Key:   candidate.CandidateKey,
					Label: candidate.Label,
					Type:  candidate.Type,
				}
			}
		}
		rows = append(rows, row)
	}

	readyCount := 0
	for _, row := range rows {
		if row.Status == batchRowStatusReady {
			readyCount++
		}
	}

	candidates := make([]batchSubtitleCandidate, 0)
	if readyCount > 0 {
		for key, count := range candidateCounts {
			if count != readyCount {
				continue
			}
			candidate := candidateMeta[key]
			candidate.Count = count
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Type != candidates[j].Type {
			return candidates[i].Type < candidates[j].Type
		}
		return candidates[i].Label < candidates[j].Label
	})

	if len(candidates) == 0 {
		for i := range rows {
			if rows[i].Status == batchRowStatusReady {
				rows[i].Status = batchRowStatusSkipped
				rows[i].Reason = "no shared subtitle candidate"
			}
		}
	}

	return batchSubtitlePreflightResponse{Candidates: candidates, Rows: rows, Summary: summarizeSubtitleRows(rows, len(candidates))}
}

func inspectSubtitleBatchFile(c *gin.Context, dep dependency.Dep, rawID string) batchSubtitleRow {
	row := batchSubtitleRow{FileID: strings.TrimSpace(rawID)}
	fileID, err := resolveFileID(dep, rawID)
	if err != nil {
		row.Status = batchRowStatusFailed
		row.Reason = err.Error()
		return row
	}

	fileModel, err := dep.FileClient().GetByID(c, fileID)
	if err != nil {
		row.Status = batchRowStatusFailed
		row.Reason = err.Error()
		return row
	}
	row.FileName = fileModel.Name

	user := inventory.UserFromContext(c)
	if user == nil {
		row.Status = batchRowStatusFailed
		row.Reason = "unauthorized"
		return row
	}

	info, _, resp := getVideoInfo(c, fileID)
	if info == nil {
		row.Status = batchRowStatusFailed
		row.Reason = responseReason(resp)
		return row
	}

	exists, err := hasPendingTaskForFile(c, dep, user.ID, queue.VideoSubtitleBurnTaskType, fileID)
	if err != nil {
		row.Status = batchRowStatusFailed
		row.Reason = err.Error()
		return row
	}
	if exists {
		row.Status = batchRowStatusConflict
		row.Reason = "subtitle burn task already exists"
		return row
	}

	candidates := buildSubtitleCandidates(fileModel.Name, info)
	if len(candidates) == 0 {
		row.Status = batchRowStatusSkipped
		row.Reason = "no subtitle candidate"
		return row
	}

	row.Candidates = candidates
	row.Status = batchRowStatusReady
	return row
}

func buildSubtitleCandidates(fileName string, info *videoInfoData) []batchSubtitleCandidateBind {
	candidates := make([]batchSubtitleCandidateBind, 0)
	seen := make(map[string]bool)
	stem := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))

	for _, ext := range info.Subtitles.External {
		key, label, ok := normalizeExternalSubtitleCandidate(stem, ext.Name)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, batchSubtitleCandidateBind{CandidateKey: key, Type: batchCandidateTypeExternal, ExternalName: ext.Name, Label: label})
	}

	for _, embedded := range info.Subtitles.Embedded {
		key, label, ok := normalizeEmbeddedSubtitleCandidate(embedded)
		if !ok || seen[key] {
			continue
		}
		idx := embedded.Index
		seen[key] = true
		candidates = append(candidates, batchSubtitleCandidateBind{CandidateKey: key, Type: batchCandidateTypeEmbedded, EmbeddedIndex: &idx, Label: label})
	}

	return candidates
}

func normalizeExternalSubtitleCandidate(videoStem string, subtitleName string) (string, string, bool) {
	subtitleBase := filepath.Base(strings.TrimSpace(subtitleName))
	ext := strings.ToLower(filepath.Ext(subtitleBase))
	if ext != ".srt" && ext != ".ass" && ext != ".ssa" {
		return "", "", false
	}
	subtitleStem := strings.TrimSuffix(subtitleBase, filepath.Ext(subtitleBase))
	if subtitleStem == videoStem {
		return "external:default", "External subtitle", true
	}
	prefix := videoStem + "."
	if !strings.HasPrefix(subtitleStem, prefix) {
		return "", "", false
	}
	lang := normalizeSubtitleToken(strings.TrimPrefix(subtitleStem, prefix))
	if lang == "" {
		return "external:default", "External subtitle", true
	}
	return "external:" + lang, "External " + lang, true
}

func normalizeEmbeddedSubtitleCandidate(subtitle videoEmbeddedSubtitle) (string, string, bool) {
	lang := normalizeSubtitleToken(subtitle.Language)
	title := strings.ToLower(strings.TrimSpace(subtitle.Title))
	text := strings.TrimSpace(lang + " " + title)
	switch {
	case strings.Contains(title, "simplified") || strings.Contains(subtitle.Title, "简体") || strings.Contains(subtitle.Title, "簡體"):
		return "embedded:simplified_chinese", "Simplified Chinese", true
	case strings.Contains(title, "hong kong"):
		return "embedded:traditional_chinese_hong_kong", "Traditional Chinese (Hong Kong)", true
	case strings.Contains(title, "taiwan"):
		return "embedded:traditional_chinese_taiwan", "Traditional Chinese (Taiwan)", true
	case strings.Contains(title, "traditional") || strings.Contains(subtitle.Title, "繁体") || strings.Contains(subtitle.Title, "繁體"):
		return "embedded:traditional_chinese", "Traditional Chinese", true
	case lang == "zht":
		return "embedded:traditional_chinese", "Traditional Chinese", true
	case lang == "chi" || lang == "zho" || lang == "zh":
		return "embedded:chinese", "Chinese", true
	case lang == "eng" || strings.Contains(title, "english"):
		return "embedded:english", "English", true
	case text != "":
		key := normalizeSubtitleToken(text)
		if key != "" {
			return "embedded:" + key, subtitleLabel(subtitle, key), true
		}
	}
	return "", "", false
}

func normalizeSubtitleToken(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range input {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteRune('_')
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func subtitleLabel(subtitle videoEmbeddedSubtitle, fallback string) string {
	if strings.TrimSpace(subtitle.Title) != "" {
		return strings.TrimSpace(subtitle.Title)
	}
	if strings.TrimSpace(subtitle.Language) != "" {
		return strings.TrimSpace(subtitle.Language)
	}
	return fallback
}

func findSubtitleBinding(candidates []batchSubtitleCandidateBind, key string) (batchSubtitleCandidateBind, bool) {
	for _, candidate := range candidates {
		if candidate.CandidateKey == key {
			return candidate, true
		}
	}
	return batchSubtitleCandidateBind{}, false
}

func subtitleOptionFromBinding(binding batchSubtitleCandidateBind) *queue.VideoSubtitleOption {
	if binding.Type == batchCandidateTypeExternal {
		return &queue.VideoSubtitleOption{Mode: queue.VideoSubtitleModeExternal, ExternalName: binding.ExternalName}
	}
	if binding.Type == batchCandidateTypeEmbedded && binding.EmbeddedIndex != nil {
		idx := *binding.EmbeddedIndex
		return &queue.VideoSubtitleOption{Mode: queue.VideoSubtitleModeEmbedded, EmbeddedIndex: &idx}
	}
	return nil
}

func createSubtitleTaskForBatch(c *gin.Context, dep dependency.Dep, user *ent.User, fileID int, option *queue.VideoSubtitleOption) (string, string, string) {
	if option == nil {
		return "", batchRowStatusSkipped, "missing subtitle option"
	}
	exists, err := hasPendingTaskForFile(c, dep, user.ID, queue.VideoSubtitleBurnTaskType, fileID)
	if err != nil {
		return "", batchRowStatusFailed, err.Error()
	}
	if exists {
		return "", batchRowStatusConflict, "subtitle burn task already exists"
	}
	if fileName, exists, err := checkBurnedOutputExists(c, dep, fileID, user.ID, option); err != nil {
		logging.FromContext(c).Warning("Video task dedup check failed: %v", err)
	} else if exists {
		return "", batchRowStatusConflict, "burned output already exists: " + fileName
	}
	t, err := queue.NewVideoSubtitleBurnTask(c, fileID, user, option)
	if err != nil {
		return "", batchRowStatusFailed, err.Error()
	}
	if err := dep.VideoProcessQueue(c).QueueTask(c, t); err != nil {
		return "", batchRowStatusFailed, err.Error()
	}
	return strconv.Itoa(t.ID()), "created", ""
}

func buildBatchHLSPreflight(c *gin.Context, rawIDs []fileIDRaw) batchHLSPreflightResponse {
	dep := dependency.FromContext(c)
	rows := make([]batchHLSRow, 0, len(rawIDs))
	for _, rawID := range rawIDs {
		rows = append(rows, inspectHLSBatchFile(c, dep, string(rawID)))
	}
	return batchHLSPreflightResponse{Rows: rows, Summary: summarizeHLSRows(rows)}
}

func inspectHLSBatchFile(c *gin.Context, dep dependency.Dep, rawID string) batchHLSRow {
	row := batchHLSRow{FileID: strings.TrimSpace(rawID)}
	fileID, err := resolveFileID(dep, rawID)
	if err != nil {
		row.Status = batchRowStatusFailed
		row.Reason = err.Error()
		return row
	}
	fileModel, err := dep.FileClient().GetByID(c, fileID)
	if err != nil {
		row.Status = batchRowStatusFailed
		row.Reason = err.Error()
		return row
	}
	row.FileName = fileModel.Name

	user := inventory.UserFromContext(c)
	if user == nil {
		row.Status = batchRowStatusFailed
		row.Reason = "unauthorized"
		return row
	}

	if hasHLSArtifact(c, dep, fileID) {
		row.Status = batchRowStatusExisting
		row.Reason = "hls artifact already exists"
		return row
	}

	exists, err := hasPendingTaskForFile(c, dep, user.ID, queue.VideoHLSSliceTaskType, fileID)
	if err != nil {
		row.Status = batchRowStatusFailed
		row.Reason = err.Error()
		return row
	}
	if exists {
		row.Status = batchRowStatusProcessing
		row.Reason = "hls task already exists"
		return row
	}

	info, _, resp := getVideoInfo(c, fileID)
	if info == nil {
		row.Status = batchRowStatusFailed
		row.Reason = responseReason(resp)
		return row
	}
	row.Codec = info.Codec
	row.AudioCodec = info.AudioCodec
	if !info.HLSCompatible {
		row.Status = batchRowStatusIncompatible
		row.Reason = "unsupported codec"
		return row
	}
	row.Status = batchRowStatusReady
	return row
}

func createHLSTaskForBatch(c *gin.Context, dep dependency.Dep, user *ent.User, fileID int) (string, string, string) {
	if hasHLSArtifact(c, dep, fileID) {
		return "", batchRowStatusSkipped, "hls artifact already exists"
	}
	exists, err := hasPendingTaskForFile(c, dep, user.ID, queue.VideoHLSSliceTaskType, fileID)
	if err != nil {
		return "", batchRowStatusFailed, err.Error()
	}
	if exists {
		return "", batchRowStatusConflict, "hls task already exists"
	}
	if status, resp, err := precheckHLSCodec(c, fileID); err != nil {
		if status == http.StatusBadRequest {
			return "", batchRowStatusSkipped, responseReason(resp)
		}
		return "", batchRowStatusFailed, responseReason(resp)
	}
	t, err := queue.NewVideoHLSSliceTask(c, fileID, user)
	if err != nil {
		return "", batchRowStatusFailed, err.Error()
	}
	if err := dep.VideoProcessQueue(c).QueueTask(c, t); err != nil {
		return "", batchRowStatusFailed, err.Error()
	}
	return strconv.Itoa(t.ID()), "created", ""
}

func hasHLSArtifact(c *gin.Context, dep dependency.Dep, fileID int) bool {
	exists, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(fileID)).Exist(c)
	return err == nil && exists
}

func summarizeSubtitleRows(rows []batchSubtitleRow, candidateCount int) batchResultSummary {
	summary := batchResultSummary{}
	for _, row := range rows {
		switch row.Status {
		case batchRowStatusReady:
			summary.Ready++
		case batchRowStatusSkipped:
			summary.Skipped++
		case batchRowStatusConflict:
			summary.Conflict++
		case batchRowStatusFailed:
			summary.Failed++
		}
	}
	if candidateCount == 0 && summary.Ready > 0 {
		summary.Skipped += summary.Ready
		summary.Ready = 0
	}
	return summary
}

func summarizeHLSRows(rows []batchHLSRow) batchResultSummary {
	summary := batchResultSummary{}
	for _, row := range rows {
		switch row.Status {
		case batchRowStatusReady:
			summary.Ready++
		case batchRowStatusExisting:
			summary.Existing++
		case batchRowStatusProcessing:
			summary.Processing++
		case batchRowStatusIncompatible:
			summary.Incompatible++
		case batchRowStatusSkipped:
			summary.Skipped++
		case batchRowStatusFailed:
			summary.Failed++
		}
	}
	return summary
}

func summarizeCreateRows(rows []batchCreateRow) batchResultSummary {
	summary := batchResultSummary{}
	for _, row := range rows {
		switch row.Status {
		case "created":
			summary.Created++
		case batchRowStatusSkipped:
			summary.Skipped++
		case batchRowStatusConflict:
			summary.Conflict++
		case batchRowStatusFailed:
			summary.Failed++
		}
	}
	return summary
}

func responseReason(resp serializer.Response) string {
	if strings.TrimSpace(resp.Error) != "" {
		return resp.Error
	}
	if strings.TrimSpace(resp.Msg) != "" {
		return resp.Msg
	}
	return "request failed"
}
