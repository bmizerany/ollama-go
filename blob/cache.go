// Package blob defines types and functions for working with
// content-addressable blobs and symbolic links to those blobs.
//
// # Blobs
//
// A blob, as defined in this package, is opaque, immutable data identified by
// the SHA-256 has of its content. This package does not attempt to interpret
// the content of blobs, nor does it provide a way to modify them.
//
// # Names
//
// Names are logical names for blobs in the form:
//
//	<host>/<namespace>/<name>/<tag>
//
// All parts of the name are required, and compared case-insensitively.
//
// # Links
//
// A link maps a name to a blob. Links are not symbolic links in the traditional
// sense, but rather a way to associate a name with a blob. Links are created
// with the [Link] method, and resolved with the [Resolve] method.
//
// # Cache Structure
//
// The cache is organized as follows:
//
//	<dir>/
//		blobs/
//			sha256-<digest> - blob content
//		manifests/
//			<host>/<namespace>/<name>/<tag> - manifest content
//
// This cache structure is designed to be compatible with older versions of
// Ollama that did not store manifests in the blob store. This is accomplished
// by copying the manifest to the blob store when it is not found in the cache
// during a call to [Resolve] for a name that does not have a digest. See
// [Resolve] for details on how this works.
//
// This structure is designed to be compatible with recent versions of Ollama,
// so it is not (for now) diverging from the existing structure, although this
// is not guaranteed to be the case in the future.
//
// Users needing to store and retrieve blobs from the cache should use the this
// package instead of interacting with the cache directly.
//
// NOTE: [GetFile] returns a path to the file in the cache on disk. The
// returned path should not be interpreted or shared across instances of
// clients, as it may change in future versions of Ollama. If two process need
// to communicate about blob location, they should share a blob digest and each
// use [GetFile] to get the path to the blob.
//
// # Digests
//
// Digests come in two forms: The canonical form, "sha256:<hex>", and the
// on-disk form, "sha256-<hex>". The canonical form is used in the API, and in display
// output. The on-disk form is used in the cache directory structure to support
// file systems that do not support colons in file names, such as Windows.
//
// [ParseDigest] will parse either form, and the [String] method
// will always return the canonical form.
package blob

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Entry represents metadata about a blob in the cache.
type Entry struct {
	Digest Digest
	Size   int64
	Time   time.Time // when added to the cache
}

// PutString takes a string or []byte and puts it into the cache as a blob. It is short
// for c.Put(d, bytes.NewReader(s), int64(len(s))).
func PutBytes[S string | []byte](c *DiskCache, d Digest, s S) error {
	return c.Put(d, bytes.NewReader([]byte(s)), int64(len(s)))
}

// DiskCache is a Cache that manages blobs and manifests on disk.
type DiskCache struct {
	// Dir specifies the top-level directory where blobs and manifest
	// pointers are stored.
	dir string
	now func() time.Time

	testHookBeforeFinalWrite func(f *os.File)
}

// Open opens a cache rooted at the given directory. If the directory does not
// exist, it is created. If the directory is not a directory, an error is
// returned.
func Open(dir string) (*DiskCache, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: fmt.Errorf("not a directory")}
	}

	subdirs := []string{"blobs", "manifests"}
	for _, subdir := range subdirs {
		if err := os.MkdirAll(filepath.Join(dir, subdir), 0777); err != nil {
			return nil, err
		}
	}

	// TODO(bmizerany): write README for hints on how to interpret the
	// cache for anyone curious and peeking in or debugging.

	// TODO(bmizerany): support shards
	c := &DiskCache{
		dir: dir,
		now: time.Now,
	}
	return c, nil
}

func readAndSum(filename string, limit int64) (data []byte, _ Digest, err error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, Digest{}, err
	}
	defer f.Close()

	h := sha256.New()
	r := io.TeeReader(f, h)
	data, err = io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil, Digest{}, err
	}
	var d Digest
	h.Sum(d.sum[:0])
	return data, d, nil
}

var debug = false

// debugger returns a function that can be used to add a step to the error message.
// The error message will be a list of steps that were taken before the error occurred.
// The steps are added in the order they are called.
//
// To set the error message, call the returned function with an empty string.
func debugger(err *error) func(step string) {
	if !debug {
		return func(string) {}
	}
	var steps []string
	return func(step string) {
		if step == "" && *err != nil {
			*err = fmt.Errorf("%q: %w", steps, *err)
			return
		}
		steps = append(steps, step)
		if len(steps) > 100 {
			// shift hints in case of a bug that causes a lot of hints
			copy(steps, steps[1:])
			steps = steps[:100]
		}
	}
}

// Resolve resolves a name of the form ("name[@digest]") to a digest.
//
// If the name if fully-qualified with a digest, the digest is returned if the
// blob exists; otherwise ErrBlobNotFound is returned.
//
// If the name is not fully-qualified, a lookup is performed to find the digest
// by hashing the contents of the manifests file, and then checking the blob store.
// If the blob does not exist, it is created, and the digest is returned. See
// "Backwards Compatability" for background on why this is necessary.
//
// It is an error if the name is not a valid name, the digest, if any, is
// invalid, or the manifest is not found in the cache.
func (c *DiskCache) Resolve(name string) (Digest, error) {
	name, digest := splitNameDigest(name)
	if digest != "" {
		d := ParseDigest(digest)
		if !d.IsValid() {
			return Digest{}, ErrInvalidDigest
		}
		return d, nil
	}

	// We want to address manifests files by digest using Get. That requires
	// them to be blobs. This cannot be directly accomplished by looking in
	// the blob store because manifests can change without Ollama knowing
	// (e.g. a user modifies a manifests by hand then pushes it to update
	// their model). We also need to support the blob caches inherited from
	// older versions of Ollama, which do not store manifests in the blob
	// store, so for these cases, we need to handle adding the manifests to
	// the blob store, just in time.
	//
	// So now we read the manifests file, hash it, and copy it to the blob
	// store if it's not already there.
	//
	// This should be cheap because manifests are small, and accessed
	// infrequently.

	file, err := c.manifestPath(name)
	if err != nil {
		return Digest{}, err
	}

	data, d, err := readAndSum(file, 1<<20)
	if err != nil {
		return Digest{}, err
	}

	// Ideally we'd read the "manifest" file as a manifest to the blob file,
	// but we are not changing this yet, so copy the manifest to the blob
	// store so it can be addressed by digest subsequent calls to Get.
	if err := PutBytes(c, d, data); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// Puts the contents of r into the cache as a blob with the given digest. The
// content must be exactly size bytes long and hash to the given digest.
//
// It protects against concurrent writes by using a lock file and ensures the
// file will never reach a size greater than size, and never equal size if the
// content does not match the hash.
func (c *DiskCache) Put(d Digest, r io.Reader, size int64) error {
	return c.copyNamedFile(c.GetFile(d), r, d, size)
}

// Get performs a lookup in the cache for the given digest. It returns an error
// if the digest is invalid, or if any was encountered while fetching the blob.
func (c *DiskCache) Get(d Digest) (Entry, error) {
	if !d.IsValid() {
		return Entry{}, fmt.Errorf("invalid digest: %v", d)
	}
	name := c.GetFile(d)
	info, err := os.Stat(name)
	if err != nil {
		return Entry{}, err
	}
	if info.Size() == 0 {
		return Entry{}, fs.ErrNotExist
	}
	return Entry{
		Digest: d,
		Size:   info.Size(),
		Time:   info.ModTime(),
	}, nil
}

// Link creates a symbolic "link" in the cache to the blob with the given
// digest. The with the provided name for later use with [Resolve].
//
// If the name or digest are invalid, an error is returned, as are any errors
// encountered while creating the link.
func (c *DiskCache) Link(name string, d Digest) error {
	if !d.IsValid() {
		return ErrInvalidDigest
	}

	manifest, err := c.manifestPath(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manifest), 0777); err != nil {
		return err
	}

	f, err := os.OpenFile(c.GetFile(d), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	// Copy manifest to cache directory.
	return c.copyNamedFile(manifest, f, d, info.Size())
}

// Unlink removes the link to the blob with the given name from the cache. If
// the link does not exist, it is a no-op. It does not remove blobs.
//
// It returns an error if the name is invalid, or if any terminal errors are
// encountered while removing the link.
func (c *DiskCache) Unlink(name string) error {
	manifest, err := c.manifestPath(name)
	if err != nil {
		return err
	}
	err = os.Remove(manifest)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// GetFile returns the absolute path to the file, in the cache, for the given
// digest.
//
// It does not check if the file exists.
//
// It panics if the digest is invalid.
func (c *DiskCache) GetFile(d Digest) string {
	if !d.IsValid() {
		panic("invalid digest")
	}
	filename := fmt.Sprintf("sha256-%x", d.sum)
	return absJoin(c.dir, "blobs", filename)
}

// manifestPath finds the first manifest file on disk that matches the given
// name using a case-insensitive comparison. If no manifest file is found, it
// returns the path where the manifest file would be if it existed.
//
// If two manifest files exists on disk that match the given name using a
// case-insensitive comparison, the one that sorts first, lexically, is
// returned.
func (c *DiskCache) manifestPath(name string) (string, error) {
	np, err := nameToPath(name)
	if err != nil {
		return "", err
	}

	maybe := path.Join("manifests", np)
	for l, err := range c.manifests() {
		if err != nil {
			return "", err
		}
		if strings.EqualFold(maybe, l) {
			return path.Join(c.dir, l), nil
		}
	}
	return path.Join(c.dir, maybe), nil
}

// manifests returns a sequence of manifests in the cache. The sequence is
// ordered by the rules of [fs.Glob].
func (c *DiskCache) manifests() iter.Seq2[string, error] {
	// TODO(bmizerany): reuse empty dirnames if exist
	return func(yield func(string, error) bool) {
		fsys := os.DirFS(c.dir)
		manifests, err := fs.Glob(fsys, "manifests/*/*/*/*")
		if err != nil {
			yield("", err)
			return
		}
		for _, manifest := range manifests {
			if !yield(manifest, nil) {
				return
			}
		}
	}
}

// copyNamedFile copies file into name, expecting it to have the given Digest
// and size, if that file is not present already.
func (c *DiskCache) copyNamedFile(name string, file io.Reader, out Digest, size int64) error {
	info, err := os.Stat(name)
	if err == nil && info.Size() == size {
		// File already exists with correct size. This is good enough.
		// We can skip expensive hash checks.
		//
		// TODO: Do the hash check, but give caller a way to skip it.
		return nil
	}

	// Copy file to cache directory.
	mode := os.O_RDWR | os.O_CREATE
	if err == nil && info.Size() > size { // shouldn't happen but fix in case
		mode |= os.O_TRUNC
	}
	f, err := os.OpenFile(name, mode, 0666)
	if err != nil {
		return err
	}
	defer f.Close()
	if size == 0 {
		// File now exists with correct size.
		// Only one possible zero-length file, so contents are OK too.
		// Early return here makes sure there's a "last byte" for code below.
		return nil
	}

	// From here on, if any of the I/O writing the file fails,
	// we make a best-effort attempt to truncate the file f
	// before returning, to avoid leaving bad bytes in the file.

	// Copy file to f, but also into h to double-check hash.
	h := sha256.New()
	w := io.MultiWriter(f, h)
	if _, err := io.CopyN(w, file, size-1); err != nil {
		f.Truncate(0)
		return err
	}
	// Check last byte before writing it; writing it will make the size match
	// what other processes expect to find and might cause them to start
	// using the file.
	buf := make([]byte, 1)
	if _, err := file.Read(buf); err != nil {
		f.Truncate(0)
		return err
	}
	h.Write(buf)
	sum := h.Sum(nil)
	if !bytes.Equal(sum, out.sum[:]) {
		f.Truncate(0)
		return fmt.Errorf("file content changed underfoot")
	}

	if c.testHookBeforeFinalWrite != nil {
		c.testHookBeforeFinalWrite(f)
	}

	// Commit cache file entry.
	if _, err := f.Write(buf); err != nil {
		f.Truncate(0)
		return err
	}
	if err := f.Close(); err != nil {
		// Data might not have been written,
		// but file may look like it is the right size.
		// To be extra careful, remove cached file.
		os.Remove(name)
		return err
	}
	os.Chtimes(name, c.now(), c.now()) // mainly for tests

	return nil
}

func splitNameDigest(s string) (name, digest string) {
	i := strings.LastIndexByte(s, '@')
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

var errInvalidName = errors.New("invalid name")

func validPartByte(c byte) bool {
	return isAlphaNum(c) || c == '_' || c == '-'
}

func isAlphaNum(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9')
}

func nameToPath(name string) (_ string, err error) {
	for i := len(name) - 1; i > 0; i-- {
		if name[i] == ':' {
			bb := []byte(name)
			bb[i] = '/'
			name = string(bb)
			break
		}
	}
	var slashes int
	var size int
	for i := range name {
		if name[i] == '/' {
			if size == 0 {
				return "", errInvalidName
			}
			slashes++
			size = 0
		} else if !validPartByte(name[i]) {
			return "", errInvalidName
		}
		size++
	}
	if size == 0 || slashes < 3 {
		return "", errInvalidName
	}
	return name, nil
}

func absJoin(pp ...string) string {
	abs, err := filepath.Abs(filepath.Join(pp...))
	if err != nil {
		// Likely a bug bug or a bad OS problem. Just panic.
		panic(err)
	}
	return abs
}
