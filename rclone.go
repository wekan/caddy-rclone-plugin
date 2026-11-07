// Package caddyrclone is a Caddy plugin that serves an rclone remote - S3, Google
// Drive, SFTP, WebDAV, a local disk or any other of rclone's backends - as a site,
// inside Caddy: rclone is built into Caddy as a library, with no second process.
//
//	files.example.org {
//		rclone s3:my-bucket/public
//	}
//
//	dav.example.org {
//		basic_auth {
//			alice <hash>
//		}
//		rclone mydrive: {
//			webdav
//		}
//	}
//
// Without webdav it serves the remote read-only: downloads with ranges, and directory
// listings. With webdav, the same URLs also take WebDAV: uploads, folders, moves and
// deletes - so put Caddy's authentication in front of it.
package caddyrclone

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
	"golang.org/x/net/webdav"

	_ "github.com/rclone/rclone/backend/all" // every rclone backend
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configfile"
	"github.com/rclone/rclone/vfs"
	"github.com/rclone/rclone/vfs/vfscommon"
)

func init() {
	caddy.RegisterModule(Handler{})
}

// Handler serves an rclone remote.
type Handler struct {
	// Remote is what to serve: "name:path" of a remote in rclone.conf, an inline
	// connection string such as ":s3,provider=AWS,env_auth=true:bucket", or a local
	// path.
	Remote string `json:"remote"`
	// Config is rclone.conf. Default: rclone's own (RCLONE_CONFIG, or
	// ~/.config/rclone/rclone.conf). rclone has one configuration per process, so
	// every rclone handler must name the same file.
	Config string `json:"config,omitempty"`
	// WebDAV also takes WebDAV requests: uploads, folders, moves, deletes.
	WebDAV bool `json:"webdav,omitempty"`
	// ReadOnly refuses every request that would change the remote, WebDAV or not.
	ReadOnly bool `json:"read_only,omitempty"`
	// Prefix is the URL path the remote is served at, e.g. /files: it is removed
	// before the path is looked up, and WebDAV answers carry it. Use it with
	// `handle /files/*`, not `handle_path`, which would hide it from WebDAV clients.
	Prefix string `json:"prefix,omitempty"`
	// CacheMode is rclone's VFS cache mode: off, minimal, writes or full. Default:
	// writes with WebDAV (many backends cannot write a file in place), off without.
	CacheMode string `json:"cache_mode,omitempty"`
	// NoListing answers 404 for directories instead of listing them.
	NoListing bool `json:"no_listing,omitempty"`

	vfs   *vfs.VFS
	files http.Handler
	dav   *webdav.Handler
	log   *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.rclone", New: func() caddy.Module { return new(Handler) }}
}

var configOnce struct {
	sync.Mutex
	done bool
	path string
}

// useConfig loads rclone.conf once per process; rclone's configuration is global.
func useConfig(path string) error {
	configOnce.Lock()
	defer configOnce.Unlock()
	if configOnce.done {
		if path != configOnce.path {
			return fmt.Errorf("rclone has one configuration per process: %q is in use, not %q", configOnce.path, path)
		}
		return nil
	}
	if path != "" {
		if err := config.SetConfigPath(path); err != nil {
			return err
		}
	}
	configfile.Install()
	configOnce.done, configOnce.path = true, path
	return nil
}

// Provision opens the remote.
func (h *Handler) Provision(ctx caddy.Context) error {
	h.log = ctx.Logger()
	if h.Remote == "" {
		return fmt.Errorf("rclone: no remote to serve")
	}
	if err := useConfig(h.Config); err != nil {
		return fmt.Errorf("rclone: %w", err)
	}
	h.Prefix = "/" + strings.Trim(h.Prefix, "/")
	if h.Prefix == "/" {
		h.Prefix = ""
	}
	f, err := fs.NewFs(context.Background(), h.Remote)
	if err != nil {
		return fmt.Errorf("rclone: %s: %w", h.Remote, err)
	}
	opt := vfscommon.Opt
	opt.ReadOnly = h.ReadOnly || !h.WebDAV
	mode := h.CacheMode
	if mode == "" {
		mode = map[bool]string{true: "writes", false: "off"}[h.WebDAV && !h.ReadOnly]
	}
	if err := opt.CacheMode.Set(mode); err != nil {
		return fmt.Errorf("rclone: cache_mode %q: %w", mode, err)
	}
	h.vfs = vfs.New(context.Background(), f, &opt)

	var files http.Handler = http.FileServer(httpFS{h.vfs, !h.NoListing})
	if h.Prefix != "" {
		files = http.StripPrefix(h.Prefix, files)
	}
	h.files = files
	if h.WebDAV {
		h.dav = &webdav.Handler{
			Prefix:     h.Prefix,
			FileSystem: davFS{h.vfs},
			LockSystem: webdav.NewMemLS(),
			Logger: func(r *http.Request, err error) {
				if err != nil {
					h.log.Debug("webdav", zap.String("method", r.Method), zap.String("path", r.URL.Path), zap.Error(err))
				}
			},
		}
	}
	h.log.Info("serving rclone remote", zap.String("remote", h.Remote), zap.Bool("webdav", h.WebDAV),
		zap.Bool("read_only", opt.ReadOnly), zap.String("cache_mode", mode))
	return nil
}

// Cleanup closes the remote: the VFS writes back what its cache still holds.
func (h *Handler) Cleanup() error {
	if h.vfs != nil {
		h.vfs.Shutdown()
		h.vfs.CleanUp()
	}
	return nil
}

var readMethods = map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true, "PROPFIND": true}

// ServeHTTP serves the remote.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if h.Prefix != "" && r.URL.Path != h.Prefix && !strings.HasPrefix(r.URL.Path, h.Prefix+"/") {
		return next.ServeHTTP(w, r)
	}
	if !readMethods[r.Method] && (h.ReadOnly || h.dav == nil) {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS"+map[bool]string{true: ", PROPFIND", false: ""}[h.dav != nil])
		return caddyhttp.Error(http.StatusMethodNotAllowed, fmt.Errorf("the rclone remote is read-only here"))
	}
	if h.dav != nil && (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		h.dav.ServeHTTP(w, r)
		return nil
	}
	// GET and HEAD: downloads with ranges, and listings - which WebDAV does not give.
	h.files.ServeHTTP(w, r)
	return nil
}

func clean(name string) string { return strings.Trim(path.Clean("/"+name), "/") }

// httpFS is the remote as an http.FileSystem: a vfs Handle has the methods of
// http.File.
type httpFS struct {
	v       *vfs.VFS
	listing bool
}

func (f httpFS) Open(name string) (http.File, error) {
	if !f.listing {
		if node, err := f.v.Stat(clean(name)); err == nil && node.IsDir() {
			if _, err := f.v.Stat(path.Join(clean(name), "index.html")); err != nil {
				return nil, os.ErrNotExist
			}
		}
	}
	return f.v.Open(clean(name))
}

// davFS is the remote as a WebDAV file system: a vfs Handle is a webdav.File.
type davFS struct{ v *vfs.VFS }

func (f davFS) Mkdir(_ context.Context, name string, perm os.FileMode) error {
	return f.v.Mkdir(clean(name), perm)
}

func (f davFS) OpenFile(_ context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	return f.v.OpenFile(clean(name), flag, perm)
}

func (f davFS) RemoveAll(_ context.Context, name string) error {
	n := clean(name)
	if n == "" {
		return os.ErrPermission // never the root of the remote
	}
	node, err := f.v.Stat(n)
	if err != nil {
		return err
	}
	if dir, ok := node.(*vfs.Dir); ok {
		return dir.RemoveAll()
	}
	return node.Remove()
}

func (f davFS) Rename(_ context.Context, oldName, newName string) error {
	return f.v.Rename(clean(oldName), clean(newName))
}

func (f davFS) Stat(_ context.Context, name string) (os.FileInfo, error) {
	return f.v.Stat(clean(name))
}

var (
	_ caddy.Provisioner           = (*Handler)(nil)
	_ caddy.CleanerUpper          = (*Handler)(nil)
	_ caddyhttp.MiddlewareHandler = (*Handler)(nil)
	_ webdav.FileSystem           = davFS{}
	_ http.FileSystem             = httpFS{}
)
