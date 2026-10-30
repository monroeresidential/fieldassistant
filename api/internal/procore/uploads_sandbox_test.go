package procore

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"strconv"
	"testing"
)

// TestSandbox_UploadRoundTrip runs one Direct File Upload against a real
// Procore project: create the upload, then post a small JPEG to its storage
// URL. It creates no RFI, so the upload expires unused. It is skipped unless
// PROCORE_SANDBOX_TOKEN (a user access token), PROCORE_SANDBOX_COMPANY_ID,
// PROCORE_SANDBOX_PROJECT_ID and PROCORE_API_URL are all set.
func TestSandbox_UploadRoundTrip(t *testing.T) {
	token := os.Getenv("PROCORE_SANDBOX_TOKEN")
	companyEnv := os.Getenv("PROCORE_SANDBOX_COMPANY_ID")
	projectEnv := os.Getenv("PROCORE_SANDBOX_PROJECT_ID")
	apiURL := os.Getenv("PROCORE_API_URL")
	if token == "" || companyEnv == "" || projectEnv == "" || apiURL == "" {
		t.Skip("set PROCORE_SANDBOX_TOKEN, PROCORE_SANDBOX_COMPANY_ID, PROCORE_SANDBOX_PROJECT_ID and PROCORE_API_URL to run the live upload check")
	}
	companyID, err := strconv.ParseInt(companyEnv, 10, 64)
	if err != nil {
		t.Fatalf("PROCORE_SANDBOX_COMPANY_ID: %v", err)
	}
	projectID, err := strconv.ParseInt(projectEnv, 10, 64)
	if err != nil {
		t.Fatalf("PROCORE_SANDBOX_PROJECT_ID: %v", err)
	}

	jpg := sandboxCheckJPEG(t)
	c := NewClient(apiURL, nil, nil, nil)
	ctx := context.Background()
	up, err := c.CreateUpload(ctx, token, companyID, projectID, UploadCreate{
		ResponseFilename:    "fieldassistant-check.jpg",
		ResponseContentType: "image/jpeg",
	})
	if err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if up.UUID == "" || up.URL == "" {
		t.Fatalf("upload lacks uuid or url: %+v", up)
	}
	t.Logf("created upload %s", up.UUID)
	if err := c.PostUpload(ctx, up, "fieldassistant-check.jpg", int64(len(jpg)), bytes.NewReader(jpg)); err != nil {
		t.Fatalf("PostUpload: %v", err)
	}
	t.Logf("posted %d bytes to upload %s", len(jpg), up.UUID)
}

// sandboxCheckJPEG returns a valid JPEG of roughly 1 KB.
func sandboxCheckJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 6), B: uint8((x ^ y) * 6), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 60}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
