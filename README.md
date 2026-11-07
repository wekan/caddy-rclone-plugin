# Caddy webserver plugin: RClone

A [Caddy](https://caddyserver.com) plugin that serves an [rclone](https://rclone.org)
remote - S3, Google Drive, OneDrive, SFTP, WebDAV, a local disk or any other of
rclone's 70+ backends - as a site, **inside Caddy**: rclone is built into Caddy as a
library ([rclone/rclone](https://github.com/rclone/rclone)), with no second process.
Part of [Weddy](https://github.com/wekan/weddy), Caddy with the WeKan, Gitea and rclone
plugins.

```caddyfile
files.example.org {
	rclone s3:my-bucket/public
}

dav.example.org {
	basic_auth {
		alice $2a$14$...
	}
	rclone mydrive: {
		webdav
	}
}
```

Without `webdav` the remote is served **read-only**: downloads (with ranges) and
directory listings. With `webdav` the same URLs also take WebDAV - uploads, folders,
moves and deletes - so put Caddy's authentication in front of it.

```
rclone [<matcher>] <remote> {
	config     <rclone.conf>
	webdav
	read_only
	prefix     <path>
	cache_mode off|minimal|writes|full
	no_listing
}
```

| | |
|---|---|
| `<remote>` | `name:path` of a remote in rclone.conf, an inline connection string such as `:s3,provider=AWS,env_auth=true:bucket`, or a local path written `:local:/srv/files` - in a Caddyfile a bare `/srv/files` right after the directive is Caddy's path matcher |
| `config` | rclone.conf; default rclone's own (`RCLONE_CONFIG`, `~/.config/rclone/rclone.conf`). rclone has one configuration per process, so every `rclone` must name the same file |
| `webdav` | also take WebDAV requests |
| `read_only` | refuse everything that would change the remote, with or without `webdav` |
| `prefix` | the URL path the remote is served at, e.g. `/files`: removed before lookup, and kept in WebDAV answers. Use it with `handle /files/*` - `handle_path` would hide the prefix from WebDAV clients |
| `cache_mode` | rclone's VFS cache; default `writes` with `webdav` (many backends cannot write a file in place), `off` without |
| `no_listing` | 404 for a directory that has no index.html |

The directive is ordered before `file_server`. Deleting the remote's root is always
refused, and paths cannot leave the remote.

## Building

```sh
xcaddy build --with github.com/wekan/caddy-rclone-plugin
```

## Tests

```sh
CGO_ENABLED=0 go test ./...
```

A real Caddy serves rclone's local backend: downloads, ranges and listings; PUT
refused without `webdav` and every write refused with `read_only`; WebDAV MKCOL, PUT,
PROPFIND with the prefix, DELETE - reaching the disk - and the root refused; and the
Caddyfile.

## License

MIT - see [LICENSE](LICENSE). rclone is MIT licensed too.

Maintainer and contributor rules are in
[WeKan's CLAUDE.md and AGENTS.md](https://github.com/wekan/wekan), which cover every
repository WeKan clones into its `.tools/` directory.
