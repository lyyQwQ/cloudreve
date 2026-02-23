package video

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/driver"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager/entitysource"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

type (
	videoInfoData struct {
		Codec         string               `json:"codec"`
		AudioCodec    string               `json:"audio_codec"`
		Resolution    string               `json:"resolution"`
		Duration      float64              `json:"duration"`
		Bitrate       int64                `json:"bitrate"`
		HLSCompatible bool                 `json:"hls_compatible"`
		Subtitles     videoSubtitleSources `json:"subtitles"`
	}

	videoSubtitleSources struct {
		External []videoExternalSubtitle `json:"external"`
		Embedded []videoEmbeddedSubtitle `json:"embedded"`
	}

	videoExternalSubtitle struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}

	videoEmbeddedSubtitle struct {
		Index    int    `json:"index"`
		Language string `json:"language"`
		Title    string `json:"title"`
	}

	ffprobePayload struct {
		Format  ffprobeFormat   `json:"format"`
		Streams []ffprobeStream `json:"streams"`
	}

	ffprobeFormat struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	}

	ffprobeStream struct {
		Index     int               `json:"index"`
		CodecName string            `json:"codec_name"`
		CodecType string            `json:"codec_type"`
		Width     int               `json:"width"`
		Height    int               `json:"height"`
		Duration  string            `json:"duration"`
		BitRate   string            `json:"bit_rate"`
		Tags      map[string]string `json:"tags"`
	}
)

func getVideoInfo(c *gin.Context, fileID int) (*videoInfoData, int, serializer.Response) {
	dep := dependency.FromContext(c)
	user := inventory.UserFromContext(c)
	if user == nil {
		return nil, http.StatusUnauthorized, serializer.Response{Code: 1, Msg: "unauthorized"}
	}

	fileModel, err := dep.FileClient().GetByID(c, fileID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, http.StatusNotFound, serializer.Response{Code: serializer.CodeNotFound, Msg: "file not found"}
		}
		return nil, http.StatusInternalServerError, serializer.Response{Code: serializer.CodeDBError, Msg: "failed to get file", Error: err.Error()}
	}

	if fileModel.OwnerID != user.ID {
		return nil, http.StatusForbidden, serializer.Response{Code: serializer.CodeNoPermissionErr, Msg: "forbidden"}
	}

	if fileModel.PrimaryEntity <= 0 {
		return nil, http.StatusNotFound, serializer.Response{Code: serializer.CodeNotFound, Msg: "file has no valid entity"}
	}

	fm := manager.NewFileManager(dep, user)
	defer fm.Recycle()

	source, err := fm.GetEntitySource(c, fileModel.PrimaryEntity)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, http.StatusNotFound, serializer.Response{Code: serializer.CodeNotFound, Msg: "file not found"}
		}
		return nil, http.StatusInternalServerError, serializer.Response{Code: serializer.CodeInternalSetting, Msg: "failed to get file source", Error: err.Error()}
	}
	defer source.Close()

	meta, probeErrOutput, err := runFFProbe(c, dep, source)
	if err != nil {
		dep.Logger().Error("ffprobe failed for file_id=%d, stderr=%s, err=%v", fileID, probeErrOutput, err)
		resp := serializer.Response{Code: serializer.CodeInternalSetting, Msg: "failed to probe video", Error: err.Error()}
		status := http.StatusInternalServerError
		switch {
		case isPermissionError(probeErrOutput) || isPermissionError(err.Error()):
			resp.Code = serializer.CodeNoPermissionErr
			resp.Msg = "failed to read file"
			status = http.StatusForbidden
		case isNotFoundError(probeErrOutput) || isNotFoundError(err.Error()):
			resp.Code = serializer.CodeNotFound
			resp.Msg = "file not found"
			status = http.StatusNotFound
		}
		return nil, status, resp
	}

	if strings.TrimSpace(probeErrOutput) != "" {
		dep.Logger().Debug("ffprobe stderr for file_id=%d: %s", fileID, probeErrOutput)
	}

	res := buildVideoInfo(meta)
	if source.IsLocal() && !source.Entity().Encrypted() {
		external, err := scanExternalSubtitles(source.LocalPath(c))
		if err != nil {
			if isPermissionError(err.Error()) {
				return nil, http.StatusForbidden, serializer.Response{Code: serializer.CodeNoPermissionErr, Msg: "failed to read file", Error: err.Error()}
			}
			dep.Logger().Warning("failed to scan external subtitles for file_id=%d: %s", fileID, err)
		} else {
			res.Subtitles.External = external
		}
	}

	res.Subtitles.Embedded = collectEmbeddedSubtitles(meta.Streams)
	res.HLSCompatible = strings.EqualFold(res.Codec, "h264")

	return res, http.StatusOK, serializer.Response{}
}

func runFFProbe(c *gin.Context, dep dependency.Dep, source entitysource.EntitySource) (*ffprobePayload, string, error) {
	input, err := resolveFFProbeInput(c, source)
	if err != nil {
		return nil, "", err
	}

	cmd := exec.CommandContext(c, dep.SettingProvider().MediaMetaFFProbePath(c),
		"-v", "warning",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		input,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	stderrText := strings.TrimSpace(stderr.String())
	if err != nil {
		return nil, stderrText, fmt.Errorf("failed to invoke ffprobe: %w (stderr: %s)", err, stderrText)
	}

	meta := &ffprobePayload{}
	if err := json.Unmarshal(stdout.Bytes(), meta); err != nil {
		return nil, stderrText, fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	return meta, stderrText, nil
}

func resolveFFProbeInput(c *gin.Context, source entitysource.EntitySource) (string, error) {
	if source.IsLocal() && !source.Entity().Encrypted() {
		return source.LocalPath(c), nil
	}

	expire := time.Now().Add(60 * time.Hour)
	url, err := source.Url(driver.WithForcePublicEndpoint(c, false), entitysource.WithNoInternalProxy(), entitysource.WithExpire(&expire))
	if err != nil {
		return "", fmt.Errorf("failed to get entity url: %w", err)
	}

	return url.Url, nil
}

func buildVideoInfo(meta *ffprobePayload) *videoInfoData {
	res := &videoInfoData{}

	var videoStream *ffprobeStream
	var audioStream *ffprobeStream
	for i := range meta.Streams {
		stream := &meta.Streams[i]
		switch strings.ToLower(stream.CodecType) {
		case "video":
			if videoStream == nil {
				videoStream = stream
			}
		case "audio":
			if audioStream == nil {
				audioStream = stream
			}
		}
	}

	if videoStream != nil {
		res.Codec = videoStream.CodecName
		if videoStream.Width > 0 && videoStream.Height > 0 {
			res.Resolution = fmt.Sprintf("%dx%d", videoStream.Width, videoStream.Height)
		}
	}

	if audioStream != nil {
		res.AudioCodec = audioStream.CodecName
	}

	res.Duration = parseFloat64(meta.Format.Duration)
	if res.Duration == 0 && videoStream != nil {
		res.Duration = parseFloat64(videoStream.Duration)
	}

	res.Bitrate = parseInt64(meta.Format.BitRate)
	if res.Bitrate == 0 && videoStream != nil {
		res.Bitrate = parseInt64(videoStream.BitRate)
	}

	return res
}

func collectEmbeddedSubtitles(streams []ffprobeStream) []videoEmbeddedSubtitle {
	res := make([]videoEmbeddedSubtitle, 0)
	index := 0
	for _, stream := range streams {
		if !strings.EqualFold(stream.CodecType, "subtitle") {
			continue
		}

		res = append(res, videoEmbeddedSubtitle{
			Index:    index,
			Language: getTag(stream.Tags, "language"),
			Title:    getTag(stream.Tags, "title"),
		})
		index++
	}

	return res
}

func scanExternalSubtitles(localPath string) ([]videoExternalSubtitle, error) {
	dirPath := filepath.Dir(localPath)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	res := make([]videoExternalSubtitle, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".srt" && ext != ".ass" && ext != ".ssa" {
			continue
		}

		res = append(res, videoExternalSubtitle{
			Name: entry.Name(),
			Path: filepath.Join(dirPath, entry.Name()),
		})
	}

	sort.Slice(res, func(i, j int) bool {
		return strings.ToLower(res[i].Name) < strings.ToLower(res[j].Name)
	})

	return res, nil
}

func getTag(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}

	return ""
}

func parseFloat64(raw string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}

	return v
}

func parseInt64(raw string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}

	return v
}

func isPermissionError(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "permission denied") || strings.Contains(s, "operation not permitted")
}

func isNotFoundError(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "no such file") || strings.Contains(s, "not found")
}
