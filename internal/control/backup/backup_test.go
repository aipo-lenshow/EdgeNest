package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	plain := []byte("the quick brown fox jumps over the lazy dog")
	sealed, err := Encrypt(plain, "correct horse battery staple")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if Detect(sealed) != KindEncrypted {
		t.Fatalf("sealed blob not detected as encrypted")
	}
	got, err := Decrypt(sealed, "correct horse battery staple")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

func TestDecryptWrongPassword(t *testing.T) {
	sealed, err := Encrypt([]byte("secret"), "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(sealed, "wrong"); err != ErrBadPassword {
		t.Fatalf("want ErrBadPassword, got %v", err)
	}
}

func TestDecryptNotEncrypted(t *testing.T) {
	if _, err := Decrypt([]byte("not an envelope"), "x"); err != ErrNotEncrypted {
		t.Fatalf("want ErrNotEncrypted, got %v", err)
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		head []byte
		want Kind
	}{
		{[]byte("SQLite format 3\x00"), KindSQLite},
		{[]byte{0x1f, 0x8b, 0x08}, KindGzip},
		{[]byte(encMagic + "junk"), KindEncrypted},
		{[]byte("random"), KindUnknown},
	}
	for _, tc := range cases {
		if got := Detect(tc.head); got != tc.want {
			t.Errorf("Detect(%q) = %v, want %v", tc.head, got, tc.want)
		}
	}
}

func TestArchiveRoundtrip(t *testing.T) {
	src := t.TempDir()
	dbPath := filepath.Join(src, "panel.db")
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00-fake-db-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	certsDir := filepath.Join(src, "certs")
	domainDir := filepath.Join(certsDir, "example.com")
	if err := os.MkdirAll(domainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(domainDir, "fullchain.pem"), []byte("CERT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(domainDir, "privkey.pem"), []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := WriteTarGz(&buf, dbPath, certsDir); err != nil {
		t.Fatalf("write: %v", err)
	}
	if Detect(buf.Bytes()) != KindGzip {
		t.Fatalf("archive not detected as gzip")
	}

	dst := t.TempDir()
	outDB := filepath.Join(dst, "restored.db")
	outCerts := filepath.Join(dst, "certs")
	if err := Extract(buf.Bytes(), outDB, outCerts); err != nil {
		t.Fatalf("extract: %v", err)
	}

	gotDB, _ := os.ReadFile(outDB)
	if string(gotDB) != "SQLite format 3\x00-fake-db-bytes" {
		t.Fatalf("db mismatch: %q", gotDB)
	}
	gotCert, _ := os.ReadFile(filepath.Join(outCerts, "example.com", "fullchain.pem"))
	if string(gotCert) != "CERT" {
		t.Fatalf("cert mismatch: %q", gotCert)
	}
	gotKey, _ := os.ReadFile(filepath.Join(outCerts, "example.com", "privkey.pem"))
	if string(gotKey) != "KEY" {
		t.Fatalf("key mismatch: %q", gotKey)
	}
}

func TestArchiveNoCertsDir(t *testing.T) {
	src := t.TempDir()
	dbPath := filepath.Join(src, "panel.db")
	if err := os.WriteFile(dbPath, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	// certsDir does not exist — must not error.
	if err := WriteTarGz(&buf, dbPath, filepath.Join(src, "nope")); err != nil {
		t.Fatalf("write without certs: %v", err)
	}
	dst := t.TempDir()
	if err := Extract(buf.Bytes(), filepath.Join(dst, "db"), filepath.Join(dst, "certs")); err != nil {
		t.Fatalf("extract: %v", err)
	}
}

func TestEncryptedArchiveRoundtrip(t *testing.T) {
	src := t.TempDir()
	dbPath := filepath.Join(src, "panel.db")
	if err := os.WriteFile(dbPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteTarGz(&buf, dbPath, ""); err != nil {
		t.Fatal(err)
	}
	sealed, err := Encrypt(buf.Bytes(), "pw")
	if err != nil {
		t.Fatal(err)
	}
	targz, err := Decrypt(sealed, "pw")
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := Extract(targz, filepath.Join(dst, "db"), filepath.Join(dst, "certs")); err != nil {
		t.Fatalf("extract decrypted: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "db"))
	if string(got) != "payload" {
		t.Fatalf("mismatch: %q", got)
	}
}

func TestSafeJoinContainsTraversal(t *testing.T) {
	base := t.TempDir()
	// "../" components are neutralized by rooting+Clean, so the result must stay
	// inside base rather than escaping it.
	for _, rel := range []string{"../escape", "../../etc/passwd", "a/../../b"} {
		got, err := safeJoin(base, rel)
		if err != nil {
			continue // rejecting is also acceptable
		}
		if got != base && !strings.HasPrefix(got, base+string(os.PathSeparator)) {
			t.Fatalf("safeJoin(%q) escaped base: %q", rel, got)
		}
	}
	if _, err := safeJoin(base, "ok/file.pem"); err != nil {
		t.Fatalf("legit path rejected: %v", err)
	}
}

func TestRebaseUnderCerts(t *testing.T) {
	got := RebaseUnderCerts("/old/data/certs/example.com/fullchain.pem", "/new/data/certs")
	want := filepath.FromSlash("/new/data/certs/example.com/fullchain.pem")
	if got != want {
		t.Errorf("rebase = %q, want %q", got, want)
	}
	// No /certs/ segment — unchanged.
	if got := RebaseUnderCerts("/etc/ssl/foo.pem", "/new/certs"); got != "/etc/ssl/foo.pem" {
		t.Errorf("unexpected rebase: %q", got)
	}
}
