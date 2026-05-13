// Package githubrelease provides an interface to GitHub Releases storage.
package githubrelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/configstruct"
	"github.com/rclone/rclone/fs/fshttp"
	"github.com/rclone/rclone/fs/hash"
)

// Register Fs with rclone
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "githubrelease",
		Description: "GitHub Releases (Flat storage)",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name:     "owner",
			Help:     "GitHub repository owner.",
			Required: true,
		}, {
			Name:     "repo",
			Help:     "GitHub repository name.",
			Required: true,
		}, {
			Name:     "tag",
			Help:     "Release tag (e.g., v1.0.0).",
			Required: true,
		}, {
			Name:      "token",
			Help:      "GitHub Personal Access Token.",
			Required:  true,
			Sensitive: true,
		}},
	})
}

// Options represent the configuration of the GitHub Release backend
type Options struct {
	Owner string `config:"owner"`
	Repo  string `config:"repo"`
	Tag   string `config:"tag"`
	Token string `config:"token"`
}

// Release represents a GitHub Release API response
type Release struct {
	ID        int64   `json:"id"`
	UploadURL string  `json:"upload_url"`
	Assets    []Asset `json:"assets"`
}

// Asset represents a GitHub Release Asset API response
type Asset struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
	URL       string    `json:"url"`
}

// Fs represents a remote GitHub Release
type Fs struct {
	name     string
	root     string
	opt      Options
	features *fs.Features
	client   *http.Client
}

// Object represents an asset on the GitHub Release
type Object struct {
	fs     *Fs
	remote string
	asset  Asset
}

// checkFlat enforces the one-level directory rule
func checkFlat(name string) error {
	if strings.Contains(name, "/") {
		return errors.New("githubrelease: directories are prohibited; only flat structure is supported")
	}
	return nil
}

// setHeaders sets common GitHub API headers
func (f *Fs) setHeaders(req *http.Request, accept string) {
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if f.opt.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.opt.Token)
	}
}

// getRelease fetches the release data from GitHub
func (f *Fs) getRelease(ctx context.Context) (*Release, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", f.opt.Owner, f.opt.Repo, f.opt.Tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	f.setHeaders(req, "application/vnd.github.v3+json")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer fs.CheckClose(resp.Body, &err)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get release: %s", resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// deleteAsset deletes an asset by ID
func (f *Fs) deleteAsset(ctx context.Context, id int64) error {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/assets/%d", f.opt.Owner, f.opt.Repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, apiURL, nil)
	if err != nil {
		return err
	}
	f.setHeaders(req, "application/vnd.github.v3+json")

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer fs.CheckClose(resp.Body, &err)

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to delete asset: %s", resp.Status)
	}
	return nil
}

// NewFs constructs a new filesystem
func NewFs(ctx context.Context, name, root string, m configmap.Mapper) (fs.Fs, error) {
	opt := new(Options)
	err := configstruct.Set(m, opt)
	if err != nil {
		return nil, err
	}

	if root != "" && root != "/" {
		return nil, errors.New("githubrelease: root paths are not supported, use empty root")
	}

	f := &Fs{
		name:   name,
		root:   "", // Enforce flat root
		opt:    *opt,
		client: fshttp.NewClient(ctx),
	}

	f.features = (&fs.Features{
		CaseInsensitive:         false,
		DuplicateFiles:          false,
		CanHaveEmptyDirectories: false, // Disallow empty folders
	}).Fill(ctx, f)

	// Validate connectivity and credentials by fetching the release
	_, err = f.getRelease(ctx)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// Name returns the name of the Fs
func (f *Fs) Name() string { return f.name }

// Root returns the root path of the Fs
func (f *Fs) Root() string { return f.root }

// String returns a string representation of the Fs
func (f *Fs) String() string {
	return fmt.Sprintf("githubrelease://%s/%s@%s", f.opt.Owner, f.opt.Repo, f.opt.Tag)
}

// Features returns the optional features supported by this Fs
func (f *Fs) Features() *fs.Features { return f.features }

// Precision denotes that setting modification times is not accurately supported on upload
func (f *Fs) Precision() time.Duration { return fs.ModTimeNotSupported }

// Hashes returns a set of hashes Provided by the Fs
func (f *Fs) Hashes() hash.Set { return hash.Set(hash.None) }

// List returns a list of items in a directory (Only root allowed)
func (f *Fs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	if dir != "" {
		return nil, fs.ErrorDirNotFound
	}

	rel, err := f.getRelease(ctx)
	if err != nil {
		return nil, err
	}

	var entries fs.DirEntries
	for _, a := range rel.Assets {
		entries = append(entries, &Object{
			fs:     f,
			remote: a.Name,
			asset:  a,
		})
	}
	return entries, nil
}

// NewObject creates a new remote Object for a given remote path
func (f *Fs) NewObject(ctx context.Context, remote string) (fs.Object, error) {
	if err := checkFlat(remote); err != nil {
		return nil, err
	}

	rel, err := f.getRelease(ctx)
	if err != nil {
		return nil, err
	}

	for _, a := range rel.Assets {
		if a.Name == remote {
			return &Object{
				fs:     f,
				remote: remote,
				asset:  a,
			}, nil
		}
	}
	return nil, fs.ErrorObjectNotFound
}

// Put uploads a new asset
func (f *Fs) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	if err := checkFlat(src.Remote()); err != nil {
		return nil, err
	}

	rel, err := f.getRelease(ctx)
	if err != nil {
		return nil, err
	}

	// GitHub Releases do not overwrite. We must explicitly delete the old asset if it exists.
	for _, a := range rel.Assets {
		if a.Name == src.Remote() {
			_ = f.deleteAsset(ctx, a.ID)
		}
	}

	uploadURLTemplate := strings.Split(rel.UploadURL, "{")[0]
	uploadURL := uploadURLTemplate + "?name=" + url.QueryEscape(src.Remote())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, in)
	if err != nil {
		return nil, err
	}

	f.setHeaders(req, "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = src.Size()

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer fs.CheckClose(resp.Body, &err)

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upload failed: %s - %s", resp.Status, string(bodyBytes))
	}

	var asset Asset
	if err := json.NewDecoder(resp.Body).Decode(&asset); err != nil {
		return nil, err
	}

	return &Object{
		fs:     f,
		remote: src.Remote(),
		asset:  asset,
	}, nil
}

// Mkdir strictly prohibits directory creation
func (f *Fs) Mkdir(ctx context.Context, dir string) error {
	if dir == "" {
		return nil // Root exists
	}
	return errors.New("githubrelease: creating folders is strictly prohibited")
}

// Rmdir strictly prohibits directory deletion
func (f *Fs) Rmdir(ctx context.Context, dir string) error {
	if dir == "" {
		return errors.New("githubrelease: cannot remove root")
	}
	return errors.New("githubrelease: directories are not supported")
}

// Purge removes all assets from the release
func (f *Fs) Purge(ctx context.Context) error {
	rel, err := f.getRelease(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range rel.Assets {
		if err := f.deleteAsset(ctx, a.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed to purge %d assets", len(errs))
	}
	return nil
}

// --- Object Methods ---

func (o *Object) String() string                        { return o.remote }
func (o *Object) Remote() string                        { return o.remote }
func (o *Object) ModTime(ctx context.Context) time.Time { return o.asset.UpdatedAt }
func (o *Object) Size() int64                           { return o.asset.Size }
func (o *Object) Fs() fs.Info                           { return o.fs }
func (o *Object) Hash(ctx context.Context, t hash.Type) (string, error) {
	return "", hash.ErrUnsupported
}
func (o *Object) Storable() bool                                    { return true }
func (o *Object) SetModTime(ctx context.Context, t time.Time) error { return fs.ErrorCantSetModTime }

// Open opens the Asset for reading (supports range requests)
func (o *Object) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/assets/%d", o.fs.opt.Owner, o.fs.opt.Repo, o.asset.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	o.fs.setHeaders(req, "application/octet-stream")
	tempHeaders := make(map[string]string)
	fs.OpenOptionAddHeaders(options, tempHeaders) // Apply offset/ranges
	for key, value := range tempHeaders {
		req.Header.Set(key, value)
	}

	resp, err := o.fs.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("failed to open asset: %s", resp.Status)
	}

	return resp.Body, nil
}

// Update updates the Asset (By recreating it)
func (o *Object) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) error {
	_, err := o.fs.Put(ctx, in, src, options...)
	return err
}

// Remove deletes the remote Asset
func (o *Object) Remove(ctx context.Context) error {
	return o.fs.deleteAsset(ctx, o.asset.ID)
}
