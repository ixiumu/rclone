// Package combine implements a backend to combine multiple remotes in a directory tree
package combine

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
)

type virtual struct {
	remote  string
	content []byte
	f       *Fs
	modTime time.Time
}

func (o *virtual) String() string { return o.remote }
func (o *virtual) Remote() string { return o.remote }
func (o *virtual) Fs() fs.Info    { return o.f }
func (o *virtual) Hash(ctx context.Context, ty hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}
func (o *virtual) Storable() bool { return false }

func (o *virtual) Size() int64                           { return int64(len(o.content)) }
func (o *virtual) ModTime(ctx context.Context) time.Time { return o.modTime }
func (o *virtual) SetModTime(ctx context.Context, t time.Time) error {
	return fs.ErrorPermissionDenied
}

func (o *virtual) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o.content)), nil
}

func (o *virtual) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	return fs.ErrorPermissionDenied
}
func (o *virtual) Remove(ctx context.Context) error {
	return fs.ErrorPermissionDenied
}
