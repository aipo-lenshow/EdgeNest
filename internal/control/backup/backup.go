// Package backup builds and unpacks panel backup archives.
//
// A backup bundles two things that together let a panel be rebuilt on the same
// machine: the SQLite database (every table — inbounds, clients, settings,
// audit log, traffic, …) and the certs/ directory (ACME/self-signed PEM files,
// which live on disk outside the DB). The archive layout is:
//
//	db                     — the VACUUM'd SQLite snapshot
//	certs/<domain>/<file>  — PEM files, mirroring the live certsDir tree
//
// The archive is a gzip'd tar. It may optionally be encrypted with a passphrase
// (argon2id-derived key + AES-256-GCM); the GCM auth tag means a wrong password
// fails cleanly instead of yielding garbage. Encryption wraps the whole tar.gz
// in an envelope: magic + salt + nonce + ciphertext.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	dbEntryName = "db"
	certsPrefix = "certs/"

	// encMagic prefixes an encrypted envelope. Fixed 12 bytes so detection is a
	// cheap prefix compare and never collides with gzip (1f 8b) or the SQLite
	// header ("SQLite format 3").
	encMagic = "EDGENESTENC1"

	// sqliteMagic is the 15-byte header of a raw (legacy) .db backup. Restore
	// still accepts those for backward compatibility.
	sqliteMagic = "SQLite format 3"
)

// argon2id parameters. 64 MiB keeps key derivation a fraction of a second on a
// VPS while staying well out of brute-force range for a passphrase.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// ErrBadPassword is returned when decryption fails — wrong passphrase or a
// corrupted/forged envelope (GCM can't tell them apart, and shouldn't).
var ErrBadPassword = errors.New("incorrect password or corrupted backup")

// ErrNotEncrypted is returned by Decrypt when the data lacks the envelope magic.
var ErrNotEncrypted = errors.New("not an encrypted backup")

// Kind classifies an uploaded backup by its leading bytes so Restore can pick
// the right path without trusting the file extension.
type Kind int

const (
	KindUnknown   Kind = iota
	KindSQLite         // raw legacy .db
	KindGzip           // plaintext tar.gz archive
	KindEncrypted      // encrypted envelope
)

// Detect inspects the first bytes of a backup file.
func Detect(head []byte) Kind {
	switch {
	case len(head) >= len(encMagic) && string(head[:len(encMagic)]) == encMagic:
		return KindEncrypted
	case len(head) >= 15 && string(head[:15]) == sqliteMagic:
		return KindSQLite
	case len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return KindGzip
	default:
		return KindUnknown
	}
}

// WriteTarGz streams a gzip'd tar of the database file plus every file under
// certsDir to w. certsDir may not exist yet (a panel with no issued certs) — it
// is simply skipped. The db file is required.
func WriteTarGz(w io.Writer, dbPath, certsDir string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, dbPath, dbEntryName, 0o600); err != nil {
		return fmt.Errorf("archive db: %w", err)
	}

	if info, err := os.Stat(certsDir); err == nil && info.IsDir() {
		walkErr := filepath.WalkDir(certsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(certsDir, path)
			if err != nil {
				return err
			}
			// tar paths are always forward-slash; mode 0600 (key material).
			name := certsPrefix + filepath.ToSlash(rel)
			return addFile(tw, path, name, 0o600)
		})
		if walkErr != nil {
			return fmt.Errorf("archive certs: %w", walkErr)
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// addFile copies one on-disk file into the tar as name.
func addFile(tw *tar.Writer, path, name string, mode int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// Extract unpacks a plaintext tar.gz (as produced by WriteTarGz) into a database
// file at dbDest and PEM files under certsDir. Returns an error if the archive
// has no "db" entry. Guards against path-traversal in cert entry names.
func Extract(targz []byte, dbDest, certsDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	sawDB := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		switch {
		case hdr.Name == dbEntryName:
			if err := writeFile(dbDest, tr, 0o600); err != nil {
				return fmt.Errorf("write db: %w", err)
			}
			sawDB = true
		case strings.HasPrefix(hdr.Name, certsPrefix):
			rel := strings.TrimPrefix(hdr.Name, certsPrefix)
			dest, err := safeJoin(certsDir, rel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			if err := writeFile(dest, tr, 0o600); err != nil {
				return fmt.Errorf("write cert %s: %w", rel, err)
			}
		default:
			// Unknown entry — ignore rather than fail (forward compat).
		}
	}
	if !sawDB {
		return errors.New("archive missing db entry")
	}
	return nil
}

// safeJoin joins base and a relative path, rejecting any result that escapes
// base (e.g. via "../"). Returns the cleaned absolute-within-base path.
func safeJoin(base, rel string) (string, error) {
	clean := filepath.Clean("/" + filepath.FromSlash(rel)) // force-rooted, strips ../
	dest := filepath.Join(base, clean)
	if dest != base && !strings.HasPrefix(dest, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in archive: %q", rel)
	}
	return dest, nil
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Encrypt wraps plaintext (a tar.gz) in an AES-256-GCM envelope keyed by a
// passphrase-derived argon2id key. Layout: magic | salt(16) | nonce(12) | ct.
func Encrypt(plaintext []byte, password string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, []byte(encMagic))

	out := make([]byte, 0, len(encMagic)+len(salt)+len(nonce)+len(ct))
	out = append(out, []byte(encMagic)...)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Decrypt reverses Encrypt. Returns ErrNotEncrypted if the magic is absent and
// ErrBadPassword if the passphrase is wrong or the data was tampered with.
func Decrypt(data []byte, password string) ([]byte, error) {
	if len(data) < len(encMagic) || string(data[:len(encMagic)]) != encMagic {
		return nil, ErrNotEncrypted
	}
	rest := data[len(encMagic):]
	if len(rest) < saltLen+12 {
		return nil, ErrBadPassword
	}
	salt := rest[:saltLen]
	rest = rest[saltLen:]
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(rest) < ns {
		return nil, ErrBadPassword
	}
	nonce, ct := rest[:ns], rest[ns:]
	pt, err := gcm.Open(nil, nonce, ct, []byte(encMagic))
	if err != nil {
		return nil, ErrBadPassword
	}
	return pt, nil
}

// RebaseUnderCerts replaces everything up to and including the last "/certs/"
// segment of p with certsDir, preserving the per-domain subtree below it. Used
// on restore so cert path columns survive a different data-dir layout. If p has
// no "/certs/" segment it is returned unchanged.
func RebaseUnderCerts(p, certsDir string) string {
	const seg = "/certs/"
	i := strings.LastIndex(p, seg)
	if i < 0 {
		return p
	}
	rel := p[i+len(seg):]
	return filepath.Join(certsDir, filepath.FromSlash(rel))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
