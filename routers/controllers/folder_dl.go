package controllers

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudreve/Cloudreve/v4/application/dependency"
	"github.com/cloudreve/Cloudreve/v4/inventory"
	"github.com/cloudreve/Cloudreve/v4/inventory/types"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/fs"
	"github.com/cloudreve/Cloudreve/v4/pkg/filemanager/manager"
	"github.com/cloudreve/Cloudreve/v4/pkg/hashid"
	"github.com/cloudreve/Cloudreve/v4/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v4/pkg/util"
	sharesvc "github.com/cloudreve/Cloudreve/v4/service/share"
	"github.com/gin-gonic/gin"
)

var folderDLResolveURL = resolveFolderDLURL

var errFolderDLTraversal = errors.New("invalid traversal path")

func FolderDL(c *gin.Context) {
	token := c.Param("token")
	rawPath := c.Param("path")

	if rawPath == "" || rawPath == "/" {
		c.Redirect(http.StatusFound, "/s/"+token)
		return
	}

	relPath, err := normalizeFolderDLPath(rawPath, c.Request.RequestURI)
	if err != nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	redirectURL, status, err := folderDLResolveURL(c, token, relPath)
	if err != nil {
		if status <= 0 {
			status = http.StatusNotFound
		}
		c.AbortWithStatus(status)
		return
	}

	c.Redirect(http.StatusFound, redirectURL)
}

func normalizeFolderDLPath(rawPath, requestURI string) (string, error) {
	lowerURI := strings.ToLower(requestURI)
	if containsEncodedPathSeparator(lowerURI) {
		return "", errFolderDLTraversal
	}

	trimmed := strings.TrimPrefix(rawPath, "/")
	unescaped, err := url.PathUnescape(trimmed)
	if err != nil {
		return "", errFolderDLTraversal
	}

	lowerUnescaped := strings.ToLower(unescaped)
	if containsEncodedPathSeparator(lowerUnescaped) {
		return "", errFolderDLTraversal
	}

	if !utf8.ValidString(unescaped) {
		return "", errFolderDLTraversal
	}

	if strings.ContainsRune(unescaped, '\x00') {
		return "", errFolderDLTraversal
	}

	if strings.Contains(unescaped, "\\") {
		return "", errFolderDLTraversal
	}

	segments := strings.Split(unescaped, "/")
	clean := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." {
			return "", errFolderDLTraversal
		}
		clean = append(clean, seg)
	}

	return path.Join(clean...), nil
}

func containsEncodedPathSeparator(input string) bool {
	return strings.Contains(input, "%2f") || strings.Contains(input, "%5c")
}

func resolveFolderDLURL(c *gin.Context, token, relPath string) (string, int, error) {
	dep := dependency.FromContext(c)
	shareID, err := dep.HashIDEncoder().Decode(token, hashid.ShareID)
	if err != nil {
		return "", http.StatusNotFound, err
	}

	anonymous, err := dep.UserClient().GetLoginUserByID(c, 0)
	if err != nil {
		return "", http.StatusNotFound, err
	}

	util.WithValue(c, inventory.UserCtx{}, anonymous)
	util.WithValue(c, hashid.ObjectIDCtx{}, shareID)

	shareInfoSvc := &sharesvc.ShareInfoService{CountViews: false}
	shareInfo, err := shareInfoSvc.Get(c)
	if err != nil {
		return "", folderDLStatusFromError(err), err
	}

	if !shareInfo.Unlocked || shareInfo.SourceType == nil || *shareInfo.SourceType != types.FileTypeFolder {
		return "", http.StatusNotFound, errors.New("share target is not downloadable folder")
	}

	shareURI, err := fs.NewUriFromString(fs.NewShareUri(token, ""))
	if err != nil {
		return "", http.StatusNotFound, err
	}

	targetURI := shareURI.JoinRaw(relPath)
	m := manager.NewFileManager(dep, anonymous)
	defer m.Recycle()

	target, err := m.Get(c, targetURI)
	if err != nil {
		return "", folderDLStatusFromError(err), err
	}

	if target == nil || target.Type() == types.FileTypeFolder {
		return "", http.StatusNotFound, errors.New("target is directory")
	}

	expire := time.Now().Add(dep.SettingProvider().EntityUrlValidDuration(c))
	urls, _, err := m.GetEntityUrls(c, []manager.GetEntityUrlArgs{{URI: targetURI}},
		fs.WithIsDownload(true),
		fs.WithUrlExpire(&expire),
	)
	if err != nil {
		return "", folderDLStatusFromError(err), err
	}
	if len(urls) == 0 || urls[0].Url == "" {
		return "", http.StatusNotFound, errors.New("empty download url")
	}

	return urls[0].Url, 0, nil
}

func folderDLStatusFromError(err error) int {
	var appErr serializer.AppError
	if errors.As(err, &appErr) {
		if appErr.ErrCode() == serializer.CodeNoPermissionErr {
			return http.StatusForbidden
		}
		if appErr.ErrCode() == serializer.CodeNotFound {
			return http.StatusNotFound
		}
	}

	return http.StatusNotFound
}
