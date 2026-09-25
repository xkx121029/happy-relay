package api

import (
	"embed"
	"io/fs"
)

// 前端构建产物由 Vite 直接输出到本目录下的 webdist，随二进制一起发布。
//
//go:embed all:webdist
var embeddedWeb embed.FS

// StaticFS 返回内嵌的前端静态资源文件系统。
func StaticFS() fs.FS {
	sub, err := fs.Sub(embeddedWeb, "webdist")
	if err != nil {
		return embeddedWeb
	}
	return sub
}