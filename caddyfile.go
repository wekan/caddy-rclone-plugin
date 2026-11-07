package caddyrclone

import (
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("rclone", parseHandler)
	httpcaddyfile.RegisterDirectiveOrder("rclone", httpcaddyfile.Before, "file_server")
}

// UnmarshalCaddyfile reads:
//
//	rclone [<matcher>] <remote> {
//		config     <rclone.conf>
//		webdav
//		read_only
//		prefix     <path>
//		cache_mode off|minimal|writes|full
//		no_listing
//	}
func (h *Handler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next() // the directive name
	if !d.NextArg() {
		return d.Err("rclone needs a remote: name:path, a :backend,option=value:path connection string, or a local " +
			"path as :local:/path (a bare /path right after the directive is Caddy's path matcher)")
	}
	h.Remote = d.Val()
	if d.NextArg() {
		return d.ArgErr()
	}
	for d.NextBlock(0) {
		switch key := d.Val(); key {
		case "webdav", "read_only", "no_listing":
			if d.NextArg() {
				return d.ArgErr()
			}
			switch key {
			case "webdav":
				h.WebDAV = true
			case "read_only":
				h.ReadOnly = true
			case "no_listing":
				h.NoListing = true
			}
		case "config", "prefix", "cache_mode":
			if !d.NextArg() {
				return d.ArgErr()
			}
			switch key {
			case "config":
				h.Config = d.Val()
			case "prefix":
				h.Prefix = d.Val()
			case "cache_mode":
				h.CacheMode = d.Val()
			}
			if d.NextArg() {
				return d.ArgErr()
			}
		default:
			return d.Errf("unknown rclone option %q", key)
		}
	}
	return nil
}

func parseHandler(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var handler Handler
	return &handler, handler.UnmarshalCaddyfile(h.Dispenser)
}

var _ caddyfile.Unmarshaler = (*Handler)(nil)
