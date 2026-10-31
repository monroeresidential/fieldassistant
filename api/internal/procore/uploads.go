package procore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// uploadTimeout bounds one storage POST, body included.
	uploadTimeout = 60 * time.Second
	// uploadFileContentType is the content type of the file part; the
	// pipeline uploads JPEG photos only.
	uploadFileContentType = "image/jpeg"
)

// errUploadSent closes the form pipe once the storage service has answered,
// so a writer still sending the form stops.
var errUploadSent = errors.New("procore: storage response received")

// UploadCreate is the body of a new Direct File Upload.
type UploadCreate struct {
	ResponseFilename    string `json:"response_filename"`
	ResponseContentType string `json:"response_content_type"`
}

// Upload is a created Direct File Upload: its uuid, which an RFI's
// question.upload_ids takes, and the storage URL and form fields the file is
// posted with. URL and Fields vary per company and over time; use them as
// given. An upload must be used within an hour of creation.
type Upload struct {
	UUID   string            `json:"uuid"`
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

// CreateUpload creates a non-segmented Direct File Upload in projectID.
func (c *Client) CreateUpload(ctx context.Context, accessToken string, companyID, projectID int64, in UploadCreate) (Upload, error) {
	var up Upload
	path := "/rest/v1.1/projects/" + strconv.FormatInt(projectID, 10) + "/uploads"
	err := c.do(ctx, accessToken, &companyID, http.MethodPost, path, nil, in, &up)
	return up, err
}

// PostUpload sends size bytes of r to up's storage URL as a multipart form:
// every entry of up.Fields (in key order), then the file under "file" with
// filename and content type image/jpeg as the last part. The form is
// streamed, not buffered, with its exact Content-Length declared, since the
// storage service refuses chunked bodies. No Authorization or Procore header
// is sent, up.URL must be https and redirects are followed only to https,
// and the request is bounded by a 60 s timeout as well as ctx.
//
// ASCII control characters (CR and LF among them) are dropped from
// filename, so it cannot break out of the file part's header.
//
// A non-2xx answer is *APIError with the body truncated as Procore errors
// are; an up.URL that is not https (nothing is sent), a refused redirect and
// a transport failure are ErrUnavailable. A
// failure reading r is returned as is. PostUpload has finished with r when it
// returns.
func (c *Client) PostUpload(ctx context.Context, up Upload, filename string, size int64, r io.Reader) error {
	if size < 0 {
		return fmt.Errorf("procore: upload size %d is negative", size)
	}
	if u, err := url.Parse(up.URL); err != nil || u.Scheme != "https" {
		scheme := ""
		if u != nil {
			scheme = u.Scheme
		}
		return fmt.Errorf("procore: upload url scheme %q is not https: %w", scheme, ErrUnavailable)
	}
	filename = stripControl(filename)
	keys := make([]string, 0, len(up.Fields))
	for k := range up.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	boundary := multipart.NewWriter(io.Discard).Boundary()

	// A dry run without the file measures the form; the file adds size.
	var cw countingWriter
	if err := writeUploadForm(&cw, boundary, up.Fields, keys, filename, size, nil); err != nil {
		return fmt.Errorf("procore: measure upload form: %w", err)
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, up.URL, pr)
	if err != nil {
		return unavailable(ctx, fmt.Errorf("procore: build upload request: %w", err))
	}
	req.ContentLength = cw.n + size
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

	var writeErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		writeErr = writeUploadForm(pw, boundary, up.Fields, keys, filename, size, r)
		pw.CloseWithError(writeErr)
	}()
	resp, err := c.uploadClient.Do(req)
	// Stop a writer the transport abandoned, then wait so r is not read
	// after return.
	pr.CloseWithError(errUploadSent)
	<-done
	if writeErr != nil && !errors.Is(writeErr, io.ErrClosedPipe) && !errors.Is(writeErr, errUploadSent) {
		if resp != nil {
			resp.Body.Close()
		}
		return fmt.Errorf("procore: read upload body: %w", writeErr)
	}
	if err != nil {
		return unavailable(ctx, fmt.Errorf("procore: post upload: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody*utf8.UTFMax))
		return &APIError{Status: resp.StatusCode, Body: errBodyPrefix(b)}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBody))
	return nil
}

// stripControl drops ASCII control characters (U+0000 to U+001F and U+007F)
// from s.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// quoteEscaper escapes a multipart Content-Disposition parameter value the
// way mime/multipart does.
var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

// writeUploadForm writes the storage form to w: the fields in keys order,
// then the file part holding size bytes of file. A nil file writes the file
// part's header only, to measure the rest of the form.
func writeUploadForm(w io.Writer, boundary string, fields map[string]string, keys []string, filename string, size int64, file io.Reader) error {
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(boundary); err != nil {
		return err
	}
	for _, k := range keys {
		if err := mw.WriteField(k, fields[k]); err != nil {
			return err
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+quoteEscaper.Replace(filename)+`"`)
	h.Set("Content-Type", uploadFileContentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if file != nil {
		if n, err := io.CopyN(part, file, size); err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("file ended after %d of %d bytes", n, size)
			}
			return err
		}
	}
	return mw.Close()
}

// countingWriter counts and discards what is written to it.
type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}
