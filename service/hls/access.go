package hls

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cloudreve/Cloudreve/v4/application/constants"
	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/ent"
	"github.com/cloudreve/Cloudreve/v4/ent/hlsartifact"
	"github.com/cloudreve/Cloudreve/v4/ent/share"
	"github.com/cloudreve/Cloudreve/v4/ent/user"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/auth"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs/dbfs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/gin-gonic/gin"
)

// normalFile 检查完整祖先链：回收文件夹中的子文件仍有数据库记录。
func normalFile(ctx context.Context, dep dependency.Dep, id int) (*ent.File, error) {
	f, err := dep.DBClient().File.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	owner, err := dep.DBClient().User.Get(ctx, f.OwnerID)
	if err != nil || owner.Status != user.StatusActive {
		return nil, fmt.Errorf("inactive owner")
	}
	current := f
	seen := map[int]bool{}
	for {
		if seen[current.ID] {
			return nil, fmt.Errorf("invalid file tree")
		}
		seen[current.ID] = true
		if current.FileChildren == 0 {
			if current.Name != inventory.RootFolderName {
				return nil, fmt.Errorf("file is in trash")
			}
			return f, nil
		}
		current, err = dep.DBClient().File.Get(ctx, current.FileChildren)
		if err != nil {
			return nil, err
		}
	}
}

func authorizeFile(c *gin.Context, dep dependency.Dep, id int, write bool) bool {
	scope := types.ScopeFilesRead
	if write {
		scope = types.ScopeFilesWrite
	}
	if err := auth.CheckScope(c, scope); err != nil {
		c.JSON(http.StatusForbidden, serializer.Response{Code: serializer.CodeNoPermissionErr, Msg: "insufficient scope"})
		return false
	}

	f, err := normalFile(c, dep, id)
	if err != nil {
		notFound(c, "source file not available")
		return false
	}
	u := inventory.UserFromContext(c)
	if u != nil && u.ID == f.OwnerID && !inventory.IsAnonymousUser(u) {
		return true
	}
	// 分享播放器必须通过原有文件管理器校验分享密码及下载能力。
	if !write && c.Request.URL.Query().Get("uri") != "" && u != nil {
		uri, err := fs.NewUriFromString(c.Request.URL.Query().Get("uri"))
		if err == nil {
			m := manager.NewFileManager(dep, u)
			defer m.Recycle()
			target, err := m.Get(c, uri, dbfs.WithRequiredCapabilities(dbfs.NavigatorCapabilityDownloadFile))
			if err == nil && target.ID() == id {
				return true
			}
		}
	}
	c.JSON(http.StatusForbidden, serializer.Response{Code: serializer.CodeNoPermissionErr, Msg: "no permission"})
	return false
}

func artifactVersion(a *ent.HLSArtifact) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(a.StoragePath)))
}

// PlaybackURL 必须在调用方完成文件权限校验后签发；更新产物、撤销直链或回收文件即失效。
func PlaybackURL(ctx context.Context, dep dependency.Dep, id, linkID int) (string, error) {
	if _, err := normalFile(ctx, dep, id); err != nil {
		return "", err
	}
	a, err := dep.DBClient().HLSArtifact.Query().Where(hlsartifact.SourceFileID(id)).Only(ctx)
	if err != nil {
		return "", err
	}
	ttl := dep.SettingProvider().EntityUrlValidDuration(ctx)
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	until := time.Now().Add(ttl)
	q := url.Values{"v": {artifactVersion(a)}, "until": {strconv.FormatInt(until.Unix(), 10)}}
	if linkID > 0 {
		q.Set("link", strconv.Itoa(linkID))
	}
	signed, err := signPlaybackURI(ctx, dep.GeneralAuth(), fmt.Sprintf("/api/v4/hls/%d/play/index.m3u8?%s", id, q.Encode()), &until)
	if err != nil {
		return "", err
	}
	return signed.String(), nil
}

func authorizePlayback(c *gin.Context, dep dependency.Dep, id int, a *ent.HLSArtifact) bool {
	if _, err := normalFile(c, dep, id); err != nil {
		notFound(c, "source file not available")
		return false
	}
	until, err := strconv.ParseInt(c.Request.URL.Query().Get("until"), 10, 64)
	u := c.Request.URL
	if err != nil || until <= time.Now().Unix() || c.Request.URL.Query().Get("v") != artifactVersion(a) || checkPlaybackURI(dep.GeneralAuth(), u) != nil {
		c.JSON(http.StatusForbidden, serializer.Response{Code: serializer.CodeCredentialInvalid, Msg: "invalid or expired HLS link"})
		return false
	}
	if !validPlaybackShare(c, dep, id) {
		notFound(c, "share not available")
		return false
	}
	if raw := c.Request.URL.Query().Get("link"); raw != "" {
		linkID, err := strconv.Atoi(raw)
		if err != nil {
			notFound(c, "direct link not found")
			return false
		}
		link, err := dep.DirectLinkClient().GetByID(c, linkID)
		if err != nil || link.FileID != id {
			notFound(c, "direct link not found")
			return false
		}
	}
	return true
}

// 分享凭据只在签发时使用，播放 URL 不携带分享密码；撤销或修改分享后旧凭据失效。
func requestPlaybackURL(c *gin.Context, dep dependency.Dep, id int) (string, error) {
	raw, err := PlaybackURL(c, dep, id, 0)
	if err != nil {
		return "", err
	}
	uri, err := fs.NewUriFromString(c.Request.URL.Query().Get("uri"))
	if err != nil || uri.FileSystem() != constants.FileSystemShare {
		return raw, nil
	}
	sh, err := dep.ShareClient().GetByHashID(c, uri.ID(""))
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Del("sign")
	q.Set("share", strconv.Itoa(sh.ID))
	q.Set("sv", shareVersion(dep, sh))
	expiry, _ := strconv.ParseInt(q.Get("until"), 10, 64)
	until := time.Unix(expiry, 0)
	if sh.Expires != nil && sh.Expires.Before(until) {
		until = *sh.Expires
	}
	q.Set("until", strconv.FormatInt(until.Unix(), 10))
	u.RawQuery = q.Encode()
	signed, err := signPlaybackURI(c, dep.GeneralAuth(), u.String(), &until)
	if err != nil {
		return "", err
	}
	return signed.String(), nil
}

func shareVersion(dep dependency.Dep, sh *ent.Share) string {
	b, _ := json.Marshal(struct {
		Password string
		Props    any
	}{sh.Password, sh.Props})
	return dep.GeneralAuth().Sign(string(b), 0)
}

func validPlaybackShare(c *gin.Context, dep dependency.Dep, id int) bool {
	raw := c.Request.URL.Query().Get("share")
	if raw == "" {
		return true
	}
	sid, err := strconv.Atoi(raw)
	if err != nil {
		return false
	}
	sh, err := dep.DBClient().Share.Query().Where(share.ID(sid)).WithFile().WithUser().Only(c)
	if err != nil || inventory.IsValidShare(sh) != nil || shareVersion(dep, sh) != c.Request.URL.Query().Get("sv") {
		return false
	}
	// 移出分享目录后，旧 URL 不能继续读取文件。
	for id != 0 {
		if id == sh.Edges.File.ID {
			return true
		}
		f, err := dep.DBClient().File.Get(c, id)
		if err != nil {
			return false
		}
		id = f.FileChildren
	}
	return false
}

// 通用 SignURI 只签路径；HLS 必须同时绑定期限、产物版本、直链及分享信息。
func signPlaybackURI(_ context.Context, signer auth.Auth, raw string, expires *time.Time) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Del("sign")
	u.RawQuery = q.Encode()
	expiry := int64(0)
	if expires != nil {
		expiry = expires.Unix()
	}
	q.Set("sign", signer.Sign(u.EscapedPath()+"?"+u.RawQuery, expiry))
	u.RawQuery = q.Encode()
	return u, nil
}

func checkPlaybackURI(signer auth.Auth, u *url.URL) error {
	q := u.Query()
	sign := q.Get("sign")
	q.Del("sign")
	return signer.Check(u.EscapedPath()+"?"+q.Encode(), sign)
}
