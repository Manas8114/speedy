package plexo_test

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"speedy/pkg/plexo"
)

func TestProbeURL_Range(t *testing.T) {
	const fileSize = 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := make([]byte, fileSize)
		for i := range data {
			data[i] = byte(i)
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"test"`)
		rng := r.Header.Get("Range")
		if rng == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", fileSize))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data[0:1])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer srv.Close()

	result, err := plexo.ProbeURL(context.Background(), srv.URL+"/testfile.bin")
	if err != nil {
		t.Fatalf("ProbeURL failed: %v", err)
	}
	if !result.SupportsRanges {
		t.Errorf("expected SupportsRanges=true, got false")
	}
	if result.TotalBytes != fileSize {
		t.Errorf("expected TotalBytes=%d, got %d", fileSize, result.TotalBytes)
	}
	if result.Filename != "testfile.bin" {
		t.Errorf("expected filename=testfile.bin, got %q", result.Filename)
	}
	t.Logf("Probe OK: %+v", result)
}

func TestAvailableInterfaces(t *testing.T) {
	ifaces, err := plexo.AvailableInterfaces()
	if err != nil {
		t.Fatalf("AvailableInterfaces failed: %v", err)
	}
	t.Logf("Found %d available interface(s):", len(ifaces))
	for _, iface := range ifaces {
		t.Logf("  %s [%s] IPs: %v", iface.Name, iface.HardwareAddr, iface.LocalIPs)
	}
}

func TestSession_Download_MultiChunk(t *testing.T) {
	const fileSize = 10 * 1024 // 10 KB

	payload := make([]byte, fileSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	expectedHash := md5.Sum(payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("ETag", `"v1"`)
		rng := r.Header.Get("Range")
		if rng == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", fileSize))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[0:1])
			return
		}
		if rng != "" {
			var start, end int
			fmt.Sscanf(rng, "bytes=%d-%d", &start, &end)
			if end >= fileSize {
				end = fileSize - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[start : end+1])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
		w.WriteHeader(http.StatusOK)
		w.Write(payload)
	}))
	defer srv.Close()

	probe, err := plexo.ProbeURL(context.Background(), srv.URL+"/bigfile.bin")
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if !probe.SupportsRanges {
		t.Skip("test server range support not detected")
	}

	dest := filepath.Join(t.TempDir(), "output.bin")
	ifaces := []plexo.InterfaceInfo{
		{Name: "loopback-mock", LocalIPs: []string{"127.0.0.1"}},
	}

	sess, err := plexo.NewSession(probe, dest, ifaces, 2048)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := sess.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for {
		stats := sess.Stats()
		if stats.State == "done" || stats.State == "cancelled" {
			break
		}
	}

	stats := sess.Stats()
	if stats.State != "done" {
		t.Fatalf("expected state=done, got %q", stats.State)
	}
	t.Logf("Download complete: %d bytes in %dms", stats.CompletedBytes, stats.ElapsedMs)

	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("opening output file: %v", err)
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("hashing output: %v", err)
	}
	var got [16]byte
	copy(got[:], h.Sum(nil))
	if got != expectedHash {
		t.Errorf("MD5 mismatch: expected %x, got %x", expectedHash, got)
	}
}
